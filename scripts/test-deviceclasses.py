#!/usr/bin/env python3

# Copyright (c) Advanced Micro Devices, Inc. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Check the DeviceClasses rendered by the Helm chart."""

import itertools
import re
import subprocess
from pathlib import Path


CHART = Path(__file__).resolve().parents[1] / "helm-charts-k8s"


def render(*, auto_partition=False, vfio=False, create=True):
    command = [
        "helm", "template", "dra", str(CHART),
        "--namespace", "gpu-system",
        "--api-versions", "resource.k8s.io/v1",
    ]
    if auto_partition:
        command += ["--set", "featureGates.AutoPartition=true"]
    if vfio:
        command += ["--set", "featureGates.VFIOPassthrough=true"]
    if not create:
        command += ["--set", "deviceClass.create=false"]
    return subprocess.run(command, check=True, capture_output=True, text=True).stdout


def device_classes(manifests):
    classes = {}
    for document in re.split(r"(?m)^---\s*$", manifests):
        if not re.search(r"(?m)^kind: DeviceClass$", document):
            continue
        match = re.search(r"(?m)^  name: ([\w.-]+)$", document)
        assert match, document
        name = match.group(1)
        assert name not in classes, f"duplicate DeviceClass: {name}"
        classes[name] = document
    return classes


def selector(document):
    lines = iter(document.splitlines())
    for line in lines:
        if line == "      expression: >-":
            parts = []
            for part in lines:
                if not part.startswith("        "):
                    break
                parts.append(part.strip())
            return " ".join(parts)
    raise AssertionError(f"missing CEL selector: {document}")


def extended_resource(document):
    match = re.search(r"(?m)^  extendedResourceName: (.+)$", document)
    return match.group(1) if match else None


subprocess.run(["helm", "lint", str(CHART), "--namespace", "gpu-system"], check=True)

for auto_partition, vfio in itertools.product((False, True), repeat=2):
    classes = device_classes(render(auto_partition=auto_partition, vfio=vfio))
    expected = {"gpu.amd.com"}
    if auto_partition:
        expected.add("gpu.amd.com-spx")
    if vfio:
        expected.add("gpu.amd.com-vfio")
    assert set(classes) == expected, (auto_partition, vfio, classes.keys())

    compute = classes["gpu.amd.com"]
    assert selector(compute) == (
        "device.driver == 'gpu.amd.com' && "
        'device.attributes["gpu.amd.com"].type in ["amdgpu", "amdgpu-partition"]'
    )
    assert extended_resource(compute) == (None if auto_partition else "amd.com/gpu")

    if auto_partition:
        spx = classes["gpu.amd.com-spx"]
        assert selector(spx) == (
            "device.driver == 'gpu.amd.com' && "
            'device.attributes["gpu.amd.com"].type == "amdgpu" && '
            '(!has(device.attributes["gpu.amd.com"].computePartition) || '
            'device.attributes["gpu.amd.com"].computePartition == "spx")'
        )
        assert extended_resource(spx) == "amd.com/gpu"
    if vfio:
        passthrough = classes["gpu.amd.com-vfio"]
        assert selector(passthrough) == (
            "device.driver == 'gpu.amd.com' && "
            'device.attributes["gpu.amd.com"].type == "vfio"'
        )
        assert extended_resource(passthrough) is None
    print(f"PASS AutoPartition={auto_partition} VFIOPassthrough={vfio}")

assert not device_classes(render(auto_partition=True, vfio=True, create=False))
print("PASS deviceClass.create=false")
