package statecontroller_test

import (
	"errors"

	"github.com/stretchr/testify/mock"
)

var errBoom = errors.New("boom")

func mock1() any { return mock.Anything }
func mock2() any { return mock.Anything }
func mock3() any { return mock.Anything }
