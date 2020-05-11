package scmp

//IMPL: implements the PID controller used for the stochastic and combi information dissemination

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/scionproto/scion/go/lib/log"
)

const (
	logEnabledPID = false
)

type PID struct {
	PrevQF             float64
	Integral           float64
	FactorProportional float64
	FactorIntegral     float64
	FactorDerivative   float64
	LastUpdate         time.Time
	SetPoint           float64
	Min                float64
	Max                float64
}

func (pid *PID) setSetPoint(newSetPoint float64) {
	pid.SetPoint = newSetPoint
}

func (pid *PID) getSetPoint() float64 {
	return pid.SetPoint
}

func (pid *PID) setFactors(p, i, d float64) {
	pid.FactorProportional = p
	pid.FactorIntegral = i
	pid.FactorDerivative = d
}

func (pid *PID) getFactors() (float64, float64, float64) {
	return pid.FactorProportional, pid.FactorIntegral, pid.FactorDerivative
}

func (pid *PID) setMinMax(min, max float64) {
	if min > max {
		log.Error("Invalid min max values, min greater than max", "min", min, "max", max)
	}
	pid.Min = min
	pid.Max = max

	if pid.Integral < min {
		pid.Integral = min
	}
	if pid.Integral > max {
		pid.Integral = max
	}
}

func (pid *PID) getMinMax() (float64, float64) {
	return pid.Min, pid.Max
}

func (pid *PID) NewControlUpdate(queueFullness float64) (int, []string) {
	//TODO: remove next line and replace variable in line after and at the end of the function that when queuefullness takes more realistic values
	queueFullnessTemp := float64(rand.Intn(100))
	err := pid.SetPoint - float64(queueFullnessTemp)
	timeDiff := float64((time.Now().Sub(pid.LastUpdate)).Seconds())

	var s []string
	if logEnabledPID {
		s = append(s, fmt.Sprintf("\n Time difference: %v Error: %v", timeDiff, err))
	}

	proportional := err * pid.FactorProportional
	pid.Integral = pid.Integral + err*timeDiff
	if pid.Integral < pid.Min {
		pid.Integral = pid.Min
	} else if pid.Integral > pid.Max {
		pid.Integral = pid.Max
	}
	integral := pid.Integral * pid.FactorIntegral

	if logEnabledPID {
		s = append(s, fmt.Sprintf("\n Proportional: %v Integral: %v", proportional, integral))
	}

	derivative := pid.FactorDerivative * (pid.PrevQF - err) / timeDiff
	pid.LastUpdate = time.Now()
	output := proportional + integral + derivative
	if output < pid.Min {
		output = pid.Min
	} else if output > pid.Max {
		output = pid.Max
	}

	if logEnabledPID {
		s = append(s, fmt.Sprintf("\n Derivative: %v, Result: %v, New QF: %v", derivative, queueFullnessTemp, output))
	}

	pid.PrevQF = queueFullnessTemp
	return int(output), s
}
