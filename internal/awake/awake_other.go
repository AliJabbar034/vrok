//go:build !darwin && !linux && !windows

package awake

func hold() (func(), error) { return nil, ErrUnsupported }
