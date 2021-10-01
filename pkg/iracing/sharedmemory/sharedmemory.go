package sharedmemory

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/alexeymaximov/go-bio/mmap"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"golang.org/x/sys/windows"
	"strings"
	"syscall"
	"time"
)

var lastTickCount int32 = IntMax

var varHeadersMap map[string]*IRSDKVarHeader = map[string]*IRSDKVarHeader{}

func RunSharedMemoryClient(ch chan IRacingTelemetry, ctrl chan string, interval time.Duration) {
	mapping, err := openMapping()
	if err != nil {
		log.DefaultLogger.Error("Error opening file mapping", "error", err)
	}
	defer closeMapping(mapping)

	hDataEvent, err := openEvent()
	if err != nil {
		log.DefaultLogger.Error("Error opening data event", "error", err)
	}

	tick := time.Tick(interval)
	for {
		select {
		case <-tick:
			//data, err := readTelemetry(mapping)
			header, err := readHeader(mapping)
			log.DefaultLogger.Debug("Header", "header", header)
			if err != nil {
				log.DefaultLogger.Warn("Error reading file mapping", "error", err)
				continue
			}
			data, err := readData(mapping, header, hDataEvent)
			log.DefaultLogger.Debug("Data", "data", fmt.Sprintf("%x", data[:32]))
			//log.DefaultLogger.Debug("Variables", "vars", varHeadersMap)
			if err != nil {
				log.DefaultLogger.Warn("Error reading file mapping", "error", err)
				continue
			}

			//ch <- *data

		case ctrlMessage := <-ctrl:
			if ctrlMessage == "stop" {
				log.DefaultLogger.Info("Stopping shared memory client")
				return
			}
		}
	}
}

func openMapping() (*mmap.Mapping, error) {
	fileSize := IRacingMemMapFileSize
	mappingNamePtr, err := windows.UTF16FromString(IRacingMemMapFileName)

	mapping, err := mmap.OpenFileMapping(INVALID_HANDLE_VALUE, 0, uintptr(fileSize), mmap.ModeReadOnly, 0, &mappingNamePtr[0])
	if err != nil {
		return nil, err
	}

	return mapping, nil
}

func openEvent() (syscall.Handle, error) {
	eventNamePtr, _ := windows.UTF16FromString(IRSDK_DATAVALIDEVENTNAME)
	hDataValidEvent, err := windows.OpenEvent(windows.SYNCHRONIZE, false, &eventNamePtr[0])
	if err != nil {
		return 0, err
	}

	return syscall.Handle(hDataValidEvent), nil
}

func waitForValidData(hDataValidEvent syscall.Handle) bool {
	event, err := syscall.WaitForSingleObject(syscall.Handle(hDataValidEvent), 100)
	if err != nil {
		return false
	}

	return event == windows.WAIT_OBJECT_0
}

func readHeader(mapping *mmap.Mapping) (*IRSDKHeader, error) {
	header := IRSDKHeader{}
	fileSize := binary.Size(header)

	b := make([]byte, fileSize)
	_, err := mapping.ReadAt(b, 0)
	if err != nil {
		return nil, err
	}

	buf := bytes.NewReader(b)
	err = binary.Read(buf, binary.LittleEndian, &header)
	if err != nil {
		return nil, err
	}

	b = make([]byte, header.SessionInfoLen)
	_, err = mapping.ReadAt(b, int64(header.SessionInfoOffset))
	if err != nil {
		return nil, err
	}
	log.DefaultLogger.Debug("Data", "data", fmt.Sprintf("%s", b[:256]))

	// Read variables headers
	varHeaderSize := binary.Size(IRSDKVarHeader{})
	for i := 0; int32(i) < header.NumVars; i++ {
		varHeader := IRSDKVarHeader{}
		b := make([]byte, varHeaderSize)
		varHeaderOffset := header.VarHeaderOffset + int32(i*varHeaderSize)
		_, err := mapping.ReadAt(b, int64(varHeaderOffset))
		if err != nil {
			return nil, err
		}
		buf := bytes.NewReader(b)
		err = binary.Read(buf, binary.LittleEndian, &varHeader)
		if err != nil {
			return nil, err
		}
		varName := strings.Trim(string(varHeader.Name[:]), "\u0000")
		if varName != "" {
			varHeadersMap[varName] = &varHeader
		}
	}

	return &header, nil
}

func readData(mapping *mmap.Mapping, header *IRSDKHeader, hDataValidEvent syscall.Handle) ([]byte, error) {
	waitForValidData(hDataValidEvent)

	if header.Status == 0 {
		lastTickCount = IntMax
		return nil, errors.New("client disconnected")
	}

	latest := 0
	for i := 1; int32(i) < header.NumBuf; i++ {
		if header.VarBuf[i].TickCount > header.VarBuf[latest].TickCount {
			latest = i
		}
	}

	// if newer than last recieved, than report new data
	if lastTickCount < header.VarBuf[latest].TickCount || true {
		log.DefaultLogger.Debug("Trying to read data")
		// try twice to get the data out
		for i := 0; i < 2; i++ {
			curTickCount := header.VarBuf[latest].TickCount
			dataLen := header.BufLen
			dataOffset := header.VarBuf[0].BufOffset
			b := make([]byte, dataLen)
			log.DefaultLogger.Debug("Trying to read data", "offset", dataOffset, "len", dataLen)

			_, err := mapping.ReadAt(b, int64(dataOffset))
			if err != nil {
				return nil, err
			}
			if curTickCount == header.VarBuf[latest].TickCount {
				lastTickCount = curTickCount
			}
			return b, nil
		}
	} else if lastTickCount > header.VarBuf[latest].TickCount {
		lastTickCount = header.VarBuf[latest].TickCount
	} else {
		log.DefaultLogger.Debug("No data to read", "lastTickCount", lastTickCount, "TickCount", header.VarBuf[latest].TickCount)
	}

	return nil, nil
}

func readTelemetry(mapping *mmap.Mapping) (*IRacingTelemetry, error) {
	data := IRacingTelemetry{}
	//fileSize := binary.Size(data)
	fileSize := IRacingMemMapFileSize

	b := make([]byte, fileSize)
	_, err := mapping.ReadAt(b, 0)
	if err != nil {
		return nil, err
	}

	buf := bytes.NewReader(b)
	err = binary.Read(buf, binary.LittleEndian, &data)
	if err != nil {
		return nil, err
	}

	return &data, nil
}

func closeMapping(mapping *mmap.Mapping) {
	err := mapping.Close()
	if err != nil {
		log.DefaultLogger.Warn("Error closing file mapping", "error", err)
	}
}
