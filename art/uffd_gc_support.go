// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

// Package art contains the Soong module types that set up ART for the kernel built in the tree.
package art

import (
	"android/soong/android"
	"android/soong/cc/config"
	"android/soong/dexpreopt"

	"custom/soong/kernel"

	"github.com/google/blueprint"
)

var pctx = android.NewPackageContext("custom/soong/art")

func init() {
	android.RegisterModuleType("uffd_gc_support", UffdGcSupportFactory)
	android.RegisterPartitionPropFile("product", func(ctx android.PathContext) android.Path { return propPath(ctx) })
}

const decisionTool = "construct_uffd_gc_flag"

// propPath is the property file with ro.dalvik.vm.enable_uffd_gc for the product build.prop.
func propPath(ctx android.PathContext) android.WritablePath {
	return android.PathForOutput(ctx, "uffd_gc", "uffd_gc.prop")
}

type uffdGcSupportProperties struct {
	// The kernel_uapi_headers module of the kernel built for the device. Its kernel_build module
	// also gives the kernel configuration.
	Kernel_headers *string
}

type uffdGcSupport struct {
	android.ModuleBase

	properties uffdGcSupportProperties
}

// UffdGcSupportFactory makes a uffd_gc_support module. The module sets whether ART uses the
// userfaultfd GC, from the configuration and the user-space headers of the kernel given by
// kernel_headers, and writes that decision as the dex2oat flag and as the property of the product
// build.prop.
func UffdGcSupportFactory() android.Module {
	module := &uffdGcSupport{}
	module.AddProperties(&module.properties)
	android.InitAndroidModule(module)
	return module
}

type kernelHeadersDepTag struct {
	blueprint.BaseDependencyTag
}

var kernelHeadersTag = kernelHeadersDepTag{}

func (u *uffdGcSupport) DepsMutator(ctx android.BottomUpMutatorContext) {
	ctx.AddHostToolDependencies(decisionTool)
	if name := u.properties.Kernel_headers; name != nil {
		ctx.AddDependency(ctx.Module(), kernelHeadersTag, *name)
	}
}

func (u *uffdGcSupport) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	if u.properties.Kernel_headers == nil {
		ctx.PropertyErrorf("kernel_headers", "is required. Set it to the name of a kernel_uapi_headers module.")
		return
	}
	var headers kernel.KernelUapiHeadersInfo
	var found bool
	ctx.VisitDirectDepsProxyWithTag(kernelHeadersTag, func(m android.ModuleProxy) {
		if headers, found = android.OtherModuleProvider(ctx, m, kernel.KernelUapiHeadersInfoProvider); !found {
			ctx.PropertyErrorf("kernel_headers", "%q is a %s module. It has to be a kernel_uapi_headers module, so set it to the name of one.",
				ctx.OtherModuleName(m), ctx.OtherModuleType(m))
		}
	})
	if !found {
		return
	}

	// The headers module declares only its stamp, since the files written by headers_install are
	// known only after it ran. The tool reads the headers below that directory, which a sandbox
	// would not contain.
	clang := config.ClangPath(ctx, "bin/clang")
	rule := android.NewRuleBuilder(pctx, ctx).SandboxDisabled().Restat()
	rule.Command().BuiltTool(decisionTool).
		FlagWithInput("--kernel-config ", headers.Config).
		FlagWithArg("--kernel-headers ", headers.Usr.Join(ctx, "include").String()).
		Implicit(headers.Stamp).
		FlagWithInput("--clang ", clang).
		FlagWithOutput("--flag-out ", dexpreopt.UffdGcFlagPath(ctx)).
		FlagWithOutput("--prop-out ", propPath(ctx))
	rule.Build("uffd_gc_support", "set whether ART uses the userfaultfd GC")
}
