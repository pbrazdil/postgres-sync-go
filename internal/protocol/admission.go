package protocol

type admissionController struct {
	initial  chan struct{}
	existing chan struct{}
}

type admissionKind string

const (
	admissionInitial  admissionKind = "initial"
	admissionExisting admissionKind = "existing"
)

func newAdmissionController(cfgLimits int, existingLimits int) *admissionController {
	return &admissionController{
		initial:  make(chan struct{}, cfgLimits),
		existing: make(chan struct{}, existingLimits),
	}
}

func (a *admissionController) acquire(kind admissionKind) (func(), bool) {
	if a == nil {
		return func() {}, true
	}

	target := a.existing
	if kind == admissionInitial {
		target = a.initial
	}

	select {
	case target <- struct{}{}:
		return func() {
			<-target
		}, true
	default:
		return nil, false
	}
}
