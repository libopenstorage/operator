package aetos

import (
	"github.com/sirupsen/logrus"
)

//
//func init() {
//	dash = Get()
//}

// LogInfoD logs the message to the dashboard and log
func LogInfoD(format string, args ...interface{}) {
	dash.Infof(format, args...)
	logrus.Infof(format, args...)
}
