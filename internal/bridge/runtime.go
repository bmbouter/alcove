// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bridge

import (
	"fmt"

	"github.com/bmbouter/alcove/internal/runtime"
)

// NewRuntime creates a Runtime implementation based on the type string.
// The optional shimBinPath is used by PodmanRuntime to volume-mount the
// shim binary into dev containers.
func NewRuntime(runtimeType string, shimBinPath string) (runtime.Runtime, error) {
	switch runtimeType {
	case "podman":
		rt := runtime.NewPodmanRuntime()
		if shimBinPath != "" {
			rt.ShimBin = shimBinPath
		}
		return rt, nil
	case "docker":
		return runtime.NewDockerRuntime(), nil
	case "kubernetes":
		return runtime.NewKubernetesRuntime()
	default:
		return nil, fmt.Errorf("unknown runtime type: %s", runtimeType)
	}
}
