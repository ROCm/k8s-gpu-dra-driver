/*
 * Copyright 2023 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/dra-example-driver/pkg/amdgpu"
)

func getDeviceName(card, renderD int) string {
	return fmt.Sprintf("gpu-%v-%v", card, renderD)
}

func parseDeviceName(name string) (int, int, error) {
	var card, renderD int
	_, err := fmt.Sscanf(name, "gpu-%d-%d", &card, &renderD)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse device name %s: %v", name, err)
	}
	return card, renderD, nil
}

func enumerateAllPossibleDevices() (AllocatableDevices, error) {
	alldevices := make(AllocatableDevices)
	allAMDGPUs := amdgpu.GetAMDGPUs()
	for pciAddr, gpuInfoMap := range allAMDGPUs {
		device := resourceapi.Device{
			Name: getDeviceName(gpuInfoMap["card"].(int), gpuInfoMap["renderD"].(int)),
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				"card": {
					IntValue: ptr.To(int64(gpuInfoMap["card"].(int))),
				},
				"renderD": {
					IntValue: ptr.To(int64(gpuInfoMap["renderD"].(int))),
				},
				"devID": {
					StringValue: ptr.To(fmt.Sprintf("%s", gpuInfoMap["devID"].(string))),
				},
				"pciAddr": {
					StringValue: ptr.To(pciAddr),
				},
				// TODO fill in more attributes as needed
				//"model": {
				//	StringValue: ptr.To("LATEST-GPU-MODEL"),
				//},
				//"driverVersion": {
				//	VersionValue: ptr.To("1.0.0"),
				//},
			},
			// TODO: fill in more attributes as needed
			Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
				"memory": {
					Value: resource.MustParse("80Gi"),
				},
			},
		}
		alldevices[device.Name] = device
	}
	klog.Infof("get gpu devices: %+v", alldevices)
	return alldevices, nil
}
