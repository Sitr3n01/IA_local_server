package monitor

import (
	"encoding/binary"
	"testing"
)

// pmlog builds an ADLPMLogDataOutput with the given supported sensors.
func pmlog(values map[int]int32) []byte {
	buffer := newPMLogBuffer()
	for sensor, value := range values {
		offset := 4 + sensor*8
		binary.LittleEndian.PutUint32(buffer[offset:], 1)
		binary.LittleEndian.PutUint32(buffer[offset+4:], uint32(value))
	}
	return buffer
}

func TestNewPMLogBufferCarriesItsOwnSize(t *testing.T) {
	buffer := newPMLogBuffer()
	if len(buffer) != adlPMLogBufferSize || binary.LittleEndian.Uint32(buffer) != adlPMLogBufferSize {
		t.Fatalf("buffer is %d bytes with size field %d, want %d", len(buffer), binary.LittleEndian.Uint32(buffer), adlPMLogBufferSize)
	}
}

func TestDecodePMLogReturnsOnlySupportedSensors(t *testing.T) {
	buffer := pmlog(map[int]int32{adlSensorBoardPower: 92, adlSensorTempEdge: 57})
	// A sensor with a value but without the supported flag must not appear.
	binary.LittleEndian.PutUint32(buffer[4+adlSensorGFXClock*8+4:], 3000)
	got := decodePMLog(buffer)
	if len(got) != 2 || got[adlSensorBoardPower] != 92 || got[adlSensorTempEdge] != 57 {
		t.Fatalf("decoded = %v", got)
	}
	if len(decodePMLog(buffer[:100])) != 0 {
		t.Fatal("a truncated buffer decoded to something")
	}
}

func TestSensorsPreferBoardPowerAndFallBackToASICPower(t *testing.T) {
	board := sensorsFromPMLog(map[int]int32{adlSensorBoardPower: 210, adlSensorASICPower: 150, adlSensorTempEdge: 61, adlSensorTempHotspot: 74, adlSensorGFXClock: 2700, adlSensorMemClock: 2505})
	if board.PowerW == nil || *board.PowerW != 210 || *board.TempC != 61 || *board.HotspotC != 74 || *board.GPUClockMHz != 2700 || *board.MemClockMHz != 2505 || board.Source != "amd-adl" {
		t.Fatalf("board = %+v", board)
	}
	asic := sensorsFromPMLog(map[int]int32{adlSensorASICPower: 40})
	if asic.PowerW == nil || *asic.PowerW != 40 {
		t.Fatalf("asic fallback = %+v", asic)
	}
}

func TestSensorsRefuseImplausibleValues(t *testing.T) {
	garbage := sensorsFromPMLog(map[int]int32{adlSensorBoardPower: 2_000_000, adlSensorTempEdge: -5, adlSensorGFXClock: 0, adlSensorTempHotspot: 900})
	if garbage.PowerW != nil || garbage.TempC != nil || garbage.GPUClockMHz != nil || garbage.HotspotC != nil {
		t.Fatalf("garbage was shown: %+v", garbage)
	}
	if garbage.Source != "" {
		t.Fatalf("source = %q for a driver that said nothing usable", garbage.Source)
	}
	// One implausible sensor does not discard the others.
	partial := sensorsFromPMLog(map[int]int32{adlSensorBoardPower: 2_000_000, adlSensorTempEdge: 55})
	if partial.PowerW != nil || partial.TempC == nil {
		t.Fatalf("partial = %+v", partial)
	}
}

func TestAdapterNamesMatchAcrossDriverSpellings(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"AMD Radeon RX 9070 XT", "AMD Radeon RX 9070 XT", true},
		{"AMD Radeon(TM) Graphics", "AMD Radeon Graphics", true},
		{"  AMD  Radeon RX 9070 XT ", "amd radeon rx 9070 xt", true},
		{"AMD Radeon RX 9070 XT", "AMD Radeon(TM) Graphics", false},
		{"", "", false},
	} {
		if got := sameAdapterName(tc.a, tc.b); got != tc.same {
			t.Errorf("sameAdapterName(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}
