// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package diagnostics

import (
	"encoding/json"
	"io"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/diskutils"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

var (
	SystemRamInfoKey  = []byte("diagSystemRamInfo")
	SystemCpuInfoKey  = []byte("diagSystemCpuInfo")
	SystemDiskInfoKey = []byte("diagSystemDiskInfo")
)

func (d *DiagnosticClient) setupSysInfoDiagnostics() {
	d.mu.Lock()
	defer d.mu.Unlock()

	sysInfo := GetSysInfo(d.dataDirPath)

	updaters := []func(tx kv.RwTx) error{
		RAMInfoUpdater(sysInfo.RAM),
		CPUInfoUpdater(sysInfo.CPU),
		DiskInfoUpdater(sysInfo.Disk),
	}

	err := d.db.Update(d.ctx, func(tx kv.RwTx) error {
		for _, updater := range updaters {
			if err := updater(tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Warn("[Diagnostics] Failed to update system info", "err", err)
	}
	d.hardwareInfo = sysInfo
}

func (d *DiagnosticClient) HardwareInfoJson(w io.Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := json.NewEncoder(w).Encode(d.hardwareInfo); err != nil {
		log.Debug("[diagnostics] HardwareInfoJson", "err", err)
	}
}

func findNodeDisk(dirPath string) string {
	return diskutils.MountPointForDirPath(dirPath)
}

func GetSysInfo(dirPath string) HardwareInfo {
	nodeDisk := findNodeDisk(dirPath)

	ramInfo := GetRAMInfo()
	diskInfo := GetDiskInfo(nodeDisk)
	cpuInfo := GetCPUInfo()

	return HardwareInfo{
		RAM:  ramInfo,
		Disk: diskInfo,
		CPU:  cpuInfo,
	}
}

func GetRAMInfo() RAMInfo {
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		return RAMInfo{}
	}
	return RAMInfo{
		Total:       vmStat.Total,
		Available:   vmStat.Available,
		Used:        vmStat.Used,
		UsedPercent: vmStat.UsedPercent,
	}
}

func GetDiskInfo(nodeDisk string) DiskInfo {
	di := DiskInfo{
		MountPoint: "/",
		Device:     "/",
	}

	partitions, err := disk.Partitions(false)
	if err == nil {
		for _, partition := range partitions {
			if partition.Mountpoint == nodeDisk {
				usage, err := disk.Usage(partition.Mountpoint)
				if err == nil {
					di.FsType = partition.Fstype
					di.Total = usage.Total
					di.Free = usage.Free
					di.MountPoint = partition.Mountpoint
					di.Device = partition.Device
				}
				break
			}
		}
	}

	di.Details, err = diskutils.DiskInfo(di.Device)
	if err != nil {
		log.Debug("[diagnostics] Failed to get disk info", "err", err)
	}

	return di
}

func GetCPUInfo() []CPUInfo {
	cpuInfo, err := cpu.Info()
	if err != nil {
		return nil
	}

	result := make([]CPUInfo, 0, len(cpuInfo))
	for _, info := range cpuInfo {
		result = append(result, CPUInfo{
			ModelName: info.ModelName,
			Cores:     info.Cores,
			Mhz:       info.Mhz,
		})
	}
	return result
}

func ReadRAMInfoFromTx(tx kv.Tx) ([]byte, error) {
	bytes, err := ReadDataFromTable(tx, kv.DiagSystemInfo, SystemRamInfoKey)
	if err != nil {
		return nil, err
	}

	return common.CopyBytes(bytes), nil
}

func ReadCPUInfoFromTx(tx kv.Tx) ([]byte, error) {
	bytes, err := ReadDataFromTable(tx, kv.DiagSystemInfo, SystemCpuInfoKey)
	if err != nil {
		return nil, err
	}

	return common.CopyBytes(bytes), nil
}

func ReadDiskInfoFromTx(tx kv.Tx) ([]byte, error) {
	bytes, err := ReadDataFromTable(tx, kv.DiagSystemInfo, SystemDiskInfoKey)
	if err != nil {
		return nil, err
	}

	return common.CopyBytes(bytes), nil
}

func RAMInfoUpdater(info RAMInfo) func(tx kv.RwTx) error {
	return PutDataToTable(kv.DiagSystemInfo, SystemRamInfoKey, info)
}

func CPUInfoUpdater(info []CPUInfo) func(tx kv.RwTx) error {
	return PutDataToTable(kv.DiagSystemInfo, SystemCpuInfoKey, info)
}

func DiskInfoUpdater(info DiskInfo) func(tx kv.RwTx) error {
	return PutDataToTable(kv.DiagSystemInfo, SystemDiskInfoKey, info)
}
