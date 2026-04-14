package pg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/petrbrazdil/pulsesync/internal/shapes"
)

const replicationStandbyTimeout = 10 * time.Second

func (r *Runtime) ensureReplication(ctx context.Context) error {
	r.mu.RLock()
	if r.replicationConn != nil {
		r.mu.RUnlock()
		return nil
	}
	r.mu.RUnlock()

	r.startupMu.Lock()
	defer r.startupMu.Unlock()

	r.mu.RLock()
	if r.replicationConn != nil {
		r.mu.RUnlock()
		return nil
	}
	pool := r.queryPool
	r.mu.RUnlock()
	if pool == nil {
		return errors.New("PulseSync engine has not been started")
	}

	if err := r.ensurePublication(ctx, pool); err != nil {
		return err
	}

	replConfig, err := pgconn.ParseConfig(r.cfg.DatabaseURL)
	if err != nil {
		return err
	}
	replConfig.RuntimeParams["replication"] = "database"

	conn, err := pgconn.ConnectConfig(ctx, replConfig)
	if err != nil {
		return err
	}

	sysident, err := pglogrepl.IdentifySystem(ctx, conn)
	if err != nil {
		_ = conn.Close(context.Background())
		return err
	}

	slotName := r.replicationSlotName()
	slot, err := pglogrepl.CreateReplicationSlot(
		ctx,
		conn,
		slotName,
		"pgoutput",
		pglogrepl.CreateReplicationSlotOptions{Temporary: true},
	)
	if err != nil {
		_ = conn.Close(context.Background())
		return err
	}

	startLSN := sysident.XLogPos
	if slot.ConsistentPoint != "" {
		if consistentPoint, parseErr := pglogrepl.ParseLSN(slot.ConsistentPoint); parseErr == nil {
			startLSN = consistentPoint
		}
	}

	if err := pglogrepl.StartReplication(
		ctx,
		conn,
		slotName,
		startLSN,
		pglogrepl.StartReplicationOptions{
			PluginArgs: []string{
				"proto_version '1'",
				fmt.Sprintf("publication_names '%s'", r.publicationName()),
			},
		},
	); err != nil {
		_ = conn.Close(context.Background())
		return err
	}

	replCtx, cancel := context.WithCancel(context.Background())

	r.mu.Lock()
	r.replicationConn = conn
	r.replicationCancel = cancel
	r.systemIdentity = sysident
	r.status = StatusActive
	r.mu.Unlock()

	r.replicationWG.Add(1)
	go func() {
		defer r.replicationWG.Done()
		r.replicationLoop(replCtx, conn, startLSN)
	}()

	return nil
}

func (r *Runtime) ensurePublication(ctx context.Context, pool *pgxpool.Pool) error {
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = $1)", r.publicationName()).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err := pool.Exec(ctx, "CREATE PUBLICATION "+r.publicationName()+" FOR ALL TABLES")
	return err
}

func (r *Runtime) replicationLoop(ctx context.Context, conn *pgconn.PgConn, clientXLogPos pglogrepl.LSN) {
	nextStandbyMessageDeadline := time.Now().Add(replicationStandbyTimeout)
	relations := map[uint32]shapes.Relation{}
	touched := map[string]shapes.Relation{}

	for {
		if time.Now().After(nextStandbyMessageDeadline) {
			if err := pglogrepl.SendStandbyStatusUpdate(
				context.Background(),
				conn,
				pglogrepl.StandbyStatusUpdate{WALWritePosition: clientXLogPos},
			); err != nil {
				r.handleReplicationError(err, conn)
				return
			}
			nextStandbyMessageDeadline = time.Now().Add(replicationStandbyTimeout)
		}

		receiveCtx, cancel := context.WithDeadline(ctx, nextStandbyMessageDeadline)
		rawMsg, err := conn.ReceiveMessage(receiveCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if pgconn.Timeout(err) {
				continue
			}
			r.handleReplicationError(err, conn)
			return
		}

		if errMsg, ok := rawMsg.(*pgproto3.ErrorResponse); ok {
			r.handleReplicationError(fmt.Errorf("received Postgres WAL error: %s", errMsg.Message), conn)
			return
		}

		msg, ok := rawMsg.(*pgproto3.CopyData)
		if !ok {
			continue
		}

		switch msg.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(msg.Data[1:])
			if err != nil {
				r.handleReplicationError(err, conn)
				return
			}
			if pkm.ServerWALEnd > clientXLogPos {
				clientXLogPos = pkm.ServerWALEnd
			}
			if pkm.ReplyRequested {
				nextStandbyMessageDeadline = time.Time{}
			}

		case pglogrepl.XLogDataByteID:
			xld, err := pglogrepl.ParseXLogData(msg.Data[1:])
			if err != nil {
				r.handleReplicationError(err, conn)
				return
			}

			commitLSN, err := r.handleLogicalMessage(xld.WALData, relations, touched)
			if err != nil {
				r.handleReplicationError(err, conn)
				return
			}
			if commitLSN > clientXLogPos {
				clientXLogPos = commitLSN
			}
			if xld.WALStart > clientXLogPos {
				clientXLogPos = xld.WALStart
			}
		}
	}
}

func (r *Runtime) handleLogicalMessage(
	walData []byte,
	relations map[uint32]shapes.Relation,
	touched map[string]shapes.Relation,
) (pglogrepl.LSN, error) {
	logicalMsg, err := pglogrepl.Parse(walData)
	if err != nil {
		return 0, err
	}

	switch msg := logicalMsg.(type) {
	case *pglogrepl.RelationMessage:
		relations[msg.RelationID] = shapes.Relation{
			Schema: msg.Namespace,
			Table:  msg.RelationName,
		}
	case *pglogrepl.InsertMessage:
		r.touchRelation(relations, touched, msg.RelationID)
	case *pglogrepl.UpdateMessage:
		r.touchRelation(relations, touched, msg.RelationID)
	case *pglogrepl.DeleteMessage:
		r.touchRelation(relations, touched, msg.RelationID)
	case *pglogrepl.TruncateMessage:
		for _, relationID := range msg.RelationIDs {
			if relation, ok := relations[relationID]; ok {
				r.shapes.InvalidateByRelation(relation)
			}
		}
	case *pglogrepl.CommitMessage:
		if err := r.refreshTouchedRelations(touched); err != nil {
			return msg.TransactionEndLSN, err
		}
		clear(touched)
		return msg.TransactionEndLSN, nil
	}

	return 0, nil
}

func (r *Runtime) refreshTouchedRelations(touched map[string]shapes.Relation) error {
	if len(touched) == 0 {
		return nil
	}

	keys := make([]string, 0, len(touched))
	for key := range touched {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		relation := touched[key]
		for _, state := range r.shapes.ActiveByRelation(relation) {
			snapshotCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			snapshot, err := r.Snapshot(snapshotCtx, shapes.SnapshotRequest{
				Definition: state.Definition,
				Mode:       shapes.SnapshotModeData,
			})
			cancel()
			if err != nil {
				var relationMissing shapes.RelationNotFoundError
				if errors.As(err, &relationMissing) {
					r.shapes.InvalidateByRelation(relation)
					break
				}
				return err
			}
			if _, _, err := r.shapes.Refresh(state.Handle, snapshot); err != nil && !errors.Is(err, shapes.ErrShapeDeleted) {
				return err
			}
		}
	}

	return nil
}

func (r *Runtime) touchRelation(relations map[uint32]shapes.Relation, touched map[string]shapes.Relation, relationID uint32) {
	relation, ok := relations[relationID]
	if !ok {
		return
	}
	touched[relationMapKey(relation)] = relation
}

func (r *Runtime) handleReplicationError(err error, conn *pgconn.PgConn) {
	_ = conn.Close(context.Background())

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.replicationConn == conn {
		r.replicationConn = nil
		r.replicationCancel = nil
		if r.status != StatusStopped {
			r.status = StatusWaiting
		}
	}
	_ = err
}

func (r *Runtime) publicationName() string {
	return compactIdentifier("pulsesync_"+r.cfg.ReplicationStreamID+"_pub", 63)
}

func (r *Runtime) replicationSlotName() string {
	base := compactIdentifier("pulsesync_"+r.cfg.ReplicationStreamID+"_slot", 48)
	return compactIdentifier(fmt.Sprintf("%s_%x", base, time.Now().UnixNano()), 63)
}

func compactIdentifier(value string, maxLen int) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}

	identifier := strings.Trim(builder.String(), "_")
	for strings.Contains(identifier, "__") {
		identifier = strings.ReplaceAll(identifier, "__", "_")
	}
	if identifier == "" {
		identifier = "pulsesync"
	}
	if identifier[0] >= '0' && identifier[0] <= '9' {
		identifier = "p_" + identifier
	}
	if len(identifier) > maxLen {
		identifier = identifier[:maxLen]
	}
	return strings.TrimRight(identifier, "_")
}

func relationMapKey(relation shapes.Relation) string {
	return relation.Schema + "." + relation.Table
}
