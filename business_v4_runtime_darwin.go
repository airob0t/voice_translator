//go:build darwin
// +build darwin

package main

import (
	"github.com/tuokentech/xingyi_client/protogen/common/event"
)

func isSessionTerminalEvent(ev event.Type) bool {
	return ev == event.Type_SessionFailed || ev == event.Type_SessionCanceled || ev == event.Type_SessionFinished
}
