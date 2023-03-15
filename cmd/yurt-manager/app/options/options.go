/*
Copyright 2023 The OpenYurt Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package options

import (
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/openyurtio/openyurt/cmd/yurt-manager/app/config"
)

// YurtManagerOptions is the main context object for the yurt-manager.
type YurtManagerOptions struct {
	Generic            *GenericOptions
	NodePoolController *NodePoolControllerOptions
}

// NewYurtManagerOptions creates a new YurtManagerOptions with a default config.
func NewYurtManagerOptions() (*YurtManagerOptions, error) {

	s := YurtManagerOptions{
		Generic:            NewGenericOptions(),
		NodePoolController: NewNodePoolControllerOptions(),
	}

	return &s, nil
}

func (y *YurtManagerOptions) Flags() cliflag.NamedFlagSets {
	fss := cliflag.NamedFlagSets{}
	y.Generic.AddFlags(fss.FlagSet("generic"))
	y.NodePoolController.AddFlags(fss.FlagSet("nodepool controller"))

	// Please Add Other controller flags @kadisi

	return fss
}

// Validate is used to validate the options and config before launching the yurt-manager
func (s *YurtManagerOptions) Validate() error {
	var errs []error
	errs = append(errs, s.Generic.Validate()...)
	errs = append(errs, s.NodePoolController.Validate()...)
	return utilerrors.NewAggregate(errs)
}

// ApplyTo fills up yurt manager config with options.
func (s *YurtManagerOptions) ApplyTo(c *config.Config) error {
	if err := s.Generic.ApplyTo(&c.ComponentConfig.Generic); err != nil {
		return err
	}
	if err := s.NodePoolController.ApplyTo(&c.ComponentConfig.NodePoolController); err != nil {
		return err
	}
	return nil
}

// Config return a yurt-manager config objective
func (s YurtManagerOptions) Config() (*config.Config, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	c := &config.Config{}
	if err := s.ApplyTo(c); err != nil {
		return nil, err
	}

	return c, nil
}