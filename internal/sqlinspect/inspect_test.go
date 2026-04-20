package sqlinspect

import "testing"

func TestContainsDependencyKeyword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{name: "simple predicate", sql: "priority >= 2", want: false},
		{name: "quoted literal", sql: "note = 'select from join'", want: false},
		{name: "line comment", sql: "priority = 1 -- select from other\nAND active = true", want: false},
		{name: "block comment", sql: "priority = 1 /* exists select */ AND active = true", want: false},
		{name: "subquery", sql: "id IN (SELECT item_id FROM item_flags)", want: true},
		{name: "exists", sql: "EXISTS (SELECT 1)", want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ContainsDependencyKeyword(tt.sql); got != tt.want {
				t.Fatalf("ContainsDependencyKeyword(%q) = %v, want %v", tt.sql, got, tt.want)
			}
		})
	}
}
