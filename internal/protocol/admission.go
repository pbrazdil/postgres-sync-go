package protocol

type admissionController struct {
	initial  chan struct{}
	existing chan struct{}
}

func newAdmissionController(cfgLimits int, existingLimits int) *admissionController {
	return &admissionController{
		initial:  make(chan struct{}, cfgLimits),
		existing: make(chan struct{}, existingLimits),
	}
}

func (a *admissionController) acquire(req ShapeRequest) (func(), bool) {
	if a == nil {
		return func() {}, true
	}

	target := a.existing
	if req.Offset == "-1" || req.Offset == "now" || req.Handle == "" {
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
