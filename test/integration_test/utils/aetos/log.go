package aetos

import (
	"github.com/sirupsen/logrus"
)

//
//func init() {
//	dash = Get()
//}

// Infof logs the message to the dashboard and log
func Infof(format string, args ...interface{}) {
	dash.Infof(format, args...)
	logrus.Infof(format, args...)
}
