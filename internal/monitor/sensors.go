package monitor

import (
	"encoding/binary"
	"strings"
)

// gpuSensors are the readings a GPU's driver publishes and Windows' performance
// counters do not carry: power, temperature and clocks. Every field is a
// pointer for the reason hardwareSample's are: "the driver did not say" reaches
// the page as a dash and never as a plausible zero.
type gpuSensors struct {
	PowerW      *float64
	TempC       *float64
	HotspotC    *float64
	GPUClockMHz *float64
	MemClockMHz *float64
	// Source names the interface the numbers came from, so the page can say
	// where a figure it shows comes from.
	Source string
}

// sensorReader is the platform half of the sensors. gpuName is the name the
// adapter reports through the kernel (hardwareSample.GPUName), which is the
// only key both the performance counters' side and the driver's side share.
type sensorReader interface {
	read(gpuName string) gpuSensors
	close()
}

// AMD's driver library returns a power-management log as an array of
// {supported, value} pairs indexed by sensor number (ADL_PMLOG_SENSORS in the
// vendor's adl_defines.h). Which index carries what was checked on the
// reference workstation (Radeon RX 9070 XT, RDNA 4) by sampling every supported
// sensor of the discrete adapter while a model loaded: the clocks, temperatures
// and activity moved with the load, and index 73 moved with them in watts
// (55 W at light desktop load, 92 W in a burst) while index 23, the ASIC power
// of earlier generations, stayed unsupported.
const (
	adlSensorGFXClock    = 1
	adlSensorMemClock    = 2
	adlSensorTempEdge    = 8
	adlSensorASICPower   = 23
	adlSensorTempHotspot = 27
	adlSensorBoardPower  = 73

	adlPMLogMaxSensors = 256
	adlPMLogBufferSize = 4 + adlPMLogMaxSensors*8
)

// Plausibility ceilings. A driver that returns garbage - a sensor index that
// means something else on a newer generation, say - is more likely to produce a
// number outside these than inside, and a wrong wattage shown with confidence
// is worse than a dash.
const (
	maxPlausiblePowerW = 1500
	maxPlausibleTempC  = 150
	maxPlausibleMHz    = 8000
)

// newPMLogBuffer is the input ADL expects: the buffer's own size in the first
// four bytes and zeros after them.
func newPMLogBuffer() []byte {
	buffer := make([]byte, adlPMLogBufferSize)
	binary.LittleEndian.PutUint32(buffer, adlPMLogBufferSize)
	return buffer
}

// decodePMLog returns the supported sensors of an ADLPMLogDataOutput.
func decodePMLog(buffer []byte) map[int]int32 {
	values := make(map[int]int32)
	if len(buffer) < adlPMLogBufferSize {
		return values
	}
	for sensor := 0; sensor < adlPMLogMaxSensors; sensor++ {
		offset := 4 + sensor*8
		if binary.LittleEndian.Uint32(buffer[offset:]) == 0 {
			continue
		}
		values[sensor] = int32(binary.LittleEndian.Uint32(buffer[offset+4:]))
	}
	return values
}

// sensorsFromPMLog picks the figures the page shows. Board power is preferred
// to ASIC power: it is what the card draws, where the second is the chip alone.
func sensorsFromPMLog(values map[int]int32) gpuSensors {
	pick := func(sensor int, ceiling int32) *float64 {
		value, ok := values[sensor]
		if !ok || value <= 0 || value > ceiling {
			return nil
		}
		return ptr(float64(value))
	}
	sensors := gpuSensors{
		TempC:       pick(adlSensorTempEdge, maxPlausibleTempC),
		HotspotC:    pick(adlSensorTempHotspot, maxPlausibleTempC),
		GPUClockMHz: pick(adlSensorGFXClock, maxPlausibleMHz),
		MemClockMHz: pick(adlSensorMemClock, maxPlausibleMHz),
	}
	if sensors.PowerW = pick(adlSensorBoardPower, maxPlausiblePowerW); sensors.PowerW == nil {
		sensors.PowerW = pick(adlSensorASICPower, maxPlausiblePowerW)
	}
	if sensors.PowerW != nil || sensors.TempC != nil || sensors.GPUClockMHz != nil {
		sensors.Source = "amd-adl"
	}
	return sensors
}

// sameAdapterName compares the driver's adapter name with the kernel's. The
// two spell the same device the same way on the reference workstation, but the
// trademark marks and padding vary between driver generations.
func sameAdapterName(a, b string) bool {
	return normalizeAdapterName(a) != "" && normalizeAdapterName(a) == normalizeAdapterName(b)
}

func normalizeAdapterName(name string) string {
	name = strings.ToLower(name)
	for _, mark := range []string{"(tm)", "(r)", "™", "®"} {
		name = strings.ReplaceAll(name, mark, "")
	}
	return strings.Join(strings.Fields(name), " ")
}
