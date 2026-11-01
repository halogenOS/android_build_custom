// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

// Package density contains the Soong module type that installs the file made for the screen
// density of the device.
package density

import (
	"path"

	"android/soong/android"
	"android/soong/etc"

	"github.com/google/blueprint/proptools"
)

func init() {
	registerBuildComponents(android.InitRegistrationContext)
}

func registerBuildComponents(ctx android.RegistrationContext) {
	ctx.RegisterModuleType("density_prebuilt_etc", DensityPrebuiltEtcFactory)
}

// sourceProperties are the prebuilt_etc properties that name the installed files, which
// density_src replaces.
var sourceProperties = []string{"src", "srcs", "dsts"}

type densityPrebuiltEtcProperties struct {
	// Density_src is the path of the file below each density folder, such as "battery_scale.png"
	// for "xxxhdpi/battery_scale.png". The module sets src to it below the folder of the device's
	// density.
	Density_src *string
}

// DensityPrebuiltEtcFactory makes a prebuilt_etc module whose load hook sets src from
// density_src and the screen density of the device.
func DensityPrebuiltEtcFactory() android.Module {
	m := etc.PrebuiltEtcFactory()
	props := &densityPrebuiltEtcProperties{}
	m.AddProperties(props)
	android.AddLoadHook(m, func(ctx android.LoadHookContext) { setDensitySrc(ctx, props) })
	return m
}

func setDensitySrc(ctx android.LoadHookContext, props *densityPrebuiltEtcProperties) {
	if !ctx.ProductSpecific() && !ctx.SocSpecific() && !ctx.DeviceSpecific() {
		ctx.ModuleErrorf("picks its file for one device, so it belongs on a device-specific partition. " +
			"The partitions shared by all devices only contain files that are the same on every device. " +
			"Set product_specific, vendor or device_specific")
	}
	for _, name := range sourceProperties {
		if ctx.ContainsProperty(name) {
			ctx.PropertyErrorf(name, "cannot be set together with density_src. Remove %s, or use prebuilt_etc", name)
		}
	}
	file := proptools.String(props.Density_src)
	if file == "" {
		ctx.PropertyErrorf("density_src", "must name the file below the density folders, such as \"battery_scale.png\"")
		return
	}
	bucket, err := android.ScreenDensityBucket(ctx.Config())
	if err != nil {
		ctx.ModuleErrorf("cannot choose the density folder: %s", err)
		return
	}
	ctx.AppendProperties(&struct{ Src *string }{proptools.StringPtr(path.Join(bucket, file))})
}
