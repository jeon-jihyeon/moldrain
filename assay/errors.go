package assay

import "errors"

var (
	// A profile that cannot bind or aggregate
	// Wrapped with the profile and the field
	ErrProfile = errors.New("invalid profile")
	// An option outside its range wrapped with the field name
	ErrOptions = errors.New("invalid options")
)
