//go:build !windows

package monitor

// No driver library is bound off Windows: the power card reads as unavailable.
type noSensors struct{}

func newSensorReader() sensorReader { return noSensors{} }

func (noSensors) read(string) gpuSensors { return gpuSensors{} }

func (noSensors) close() {}
