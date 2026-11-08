# build/custom

Build-system glue for the product layer: Soong module types for kernels, for the garbage collector
used by ART, and for files chosen by screen density, and board_functions.mk with Make functions for
board configurations. Product configuration lives in product/custom.

## Building a kernel for a device

A kernel for a device is built from source by a `kernel_build` module in the device tree's
Android.bp. Its properties:

* `kernel_dir`: the kernel source, relative to the top of the source tree
* `extra_source_dirs`: directories reached by the kernel source through symbolic links, such as the
  device tree sources of a Qualcomm kernel
* `kernel_arch`: the kernel's name of the architecture, passed as `ARCH=`: arm, arm64, riscv or x86
* `image`: the image target, such as `Image`
* `defconfig` and `config_fragments`: the base configuration and the fragments merged into it in
  order, relative to `arch/<kernel_arch>/configs` of the kernel source
* `clang_dir`: the clang directory, relative to the top of the source tree and below
  prebuilts/clang, such as `prebuilts/clang/host/linux-x86/clang-r547379`; without it the
  platform's clang
* `external_modules`: `root`, the external module tree, and `dirs`, the module directories below
  it, built in this order
* `module_renames`: renames of kernel modules by exact base name, each `from.ko:to.ko`
* `dt_images`: `merge`, `qcom` to merge the techpack overlays of a Qualcomm kernel or `none`, and
  `dtbo_page_size`, the page size of dtbo.img; with it the module also builds dtb.img and dtbo.img

The board names the module in `BOARD_KERNEL_BUILD_MODULE`, as `//<namespace>:<name>`, or
`:<name>` for a module of the root namespace. The device tree's namespace has to be in
`PRODUCT_SOONG_NAMESPACES`. The named module installs the kernel, and dtb.img when the board sets
`BOARD_INCLUDE_DTB_IN_BOOTIMG`, and hands dtbo.img to Make, where the boot, vendor_boot and dtbo
images take them. The board sets none of AOSP's kernel module list variables,
`BOARD_<image>_KERNEL_MODULES` and its forms ending in `_ARCHIVE`, `_LOAD`, `_OPTIONS_FILE` and
`_BLOCKLIST_FILE`, each with or without a kernel module directory suffix: some of them make Kati
install files also installed by the Soong modules. The build does not check this.

The kernel modules reach their partitions through `kernel_partition_modules` modules, which take the
place of AOSP's `prebuilt_kernel_modules` for a kernel built by `kernel_build`. Each takes the
kernel's `.modules` zip as `zip.src` and its `.system_map` as `system_map`, installs every module of
the zip with `zip.install_all` or the modules listed in its `load_list`, `recovery_load_list` and
`install_list`, and checks them against the kernel's System.map. With `zip.install_all`, a zip
without modules fails the build unless `zip.allow_empty` is set. A module with `ramdisk: true`
installs into `lib/modules` of the boot ramdisk. `install_list` has the form of a load list, and its
modules are not written to `modules.load`. A `kernel_partition_modules` module not listed by any
product in `PRODUCT_PACKAGES` installs nothing into its partition, so the partition is built without
those modules, and the kernel build adds no check that reports it. Each of these modules replaces
its install directory as a whole, and Soong reports any of its own modules or `PRODUCT_COPY_FILES`
entry that installs into that directory; a Make module of an `Android.mk` that installs into it is
not checked, since Soong does not see Make modules.

cc modules get the user-space headers of the kernel from `kernel_uapi_headers` modules, such as
`generated_kernel_includes`, which uses the kernel named by `BOARD_KERNEL_BUILD_MODULE`. Such a
module copies every header installed by `headers_install` below `usr` and exports `usr/include`. A
`kernel_uapi_header_dirs` module exports chosen folders below `usr` of a `kernel_uapi_headers`
module as generated headers: `headers` is the name of that module, and `dirs` lists the folders in
the order in which they are exported, each a clean relative path listed once. It does not install
any header itself.

The output `.config` of a `kernel_build` module, such as `:Pong-kernel{.config}`, is the
configuration used to build the kernel: the defconfig with every fragment merged into it. A separate
rule makes it before the kernel build, so a rule that reads only the configuration does not wait for
the kernel to compile. A `kernel_uapi_headers` module passes it on together with the headers.

The kernel parts for Pong, as its device tree gives them:

```
kernel_build {
    name: "Pong-kernel",
    kernel_dir: "kernel/nothing/sm8475",
    extra_source_dirs: ["kernel/nothing/sm8475-devicetrees"],
    kernel_arch: "arm64",
    image: "Image",
    defconfig: "gki_defconfig",
    config_fragments: [
        "vendor/waipio_GKI.config",
        "vendor/nothing/waipio_GKI.config",
        "vendor/debugfs.config",
        "astatine.config",
    ],
    licenses: ["linux_kernel_license"],
    external_modules: {
        root: "kernel/nothing/sm8475-modules",
        dirs: [
            "qcom/opensource/mmrm-driver",
            "qcom/opensource/audio-kernel",
            "qcom/opensource/camera-kernel",
            "qcom/opensource/cvp-kernel",
            "qcom/opensource/dataipa/drivers/platform/msm",
            "qcom/opensource/datarmnet/core",
            "qcom/opensource/datarmnet-ext/aps",
            "qcom/opensource/datarmnet-ext/offload",
            "qcom/opensource/datarmnet-ext/shs",
            "qcom/opensource/datarmnet-ext/perf",
            "qcom/opensource/datarmnet-ext/perf_tether",
            "qcom/opensource/datarmnet-ext/sch",
            "qcom/opensource/datarmnet-ext/wlan",
            "qcom/opensource/display-drivers/msm",
            "qcom/opensource/eva-kernel",
            "qcom/opensource/video-driver",
            "qcom/opensource/wlan/qcacld-3.0/.qca6490",
        ],
    },
    dt_images: {
        merge: "qcom",
        dtbo_page_size: 4096,
    },
}

kernel_partition_modules {
    name: "Pong-kernel-modules-vendor_dlkm",
    licenses: ["linux_kernel_license"],
    vendor_dlkm_specific: true,
    zip: {
        src: ":Pong-kernel{.modules}",
        install_all: true,
    },
    load_list: "modules.load",
    blocklist_file: "modules.blocklist",
    system_map: ":Pong-kernel{.system_map}",
}

kernel_partition_modules {
    name: "Pong-kernel-modules-vendor_ramdisk",
    licenses: ["linux_kernel_license"],
    vendor_ramdisk: true,
    zip: {
        src: ":Pong-kernel{.modules}",
    },
    load_list: "modules.load.vendor_boot",
    recovery_load_list: "modules.load.recovery",
    blocklist_file: "modules.blocklist",
    system_map: ":Pong-kernel{.system_map}",
}
```

with `BOARD_KERNEL_BUILD_MODULE := //device/nothing/Pong:Pong-kernel` in its board configuration
and the two partition modules in `PRODUCT_PACKAGES`.

A board can instead keep the LineageOS-style kernel variables, such as `TARGET_KERNEL_SOURCE`,
`TARGET_KERNEL_CONFIG`, `TARGET_KERNEL_EXT_MODULES` and the module load lists, and leave
`BOARD_KERNEL_BUILD_MODULE` unset. BoardConfigKernel.mk in product/custom checks these variables,
passes them to Soong in the config namespace `board_kernel`, sets `BOARD_KERNEL_BUILD_MODULE` to
`:board_kernel` and empties the module list variables taken over by the created modules. The
module `board_kernel_adapter`, listed by every device product in `PRODUCT_PACKAGES`, then creates
the `kernel_build` module `board_kernel` and the `kernel_partition_modules` modules
`board_kernel_modules_<partition>` for vendor_dlkm, or vendor without a vendor_dlkm image, and for
the vendor ramdisk, or the boot ramdisk without a vendor_boot image, and requires the partition
modules. The module for vendor_dlkm or vendor installs every module built by the kernel. The
ramdisk module installs the modules listed in `BOOT_KERNEL_MODULES` and the two vendor ramdisk
load lists, and the board gets it when it sets any of the three. The `extra_source_dirs` of
`board_kernel` are the directories outside the kernel source reached by the links directly
inside `arch/<arch>/boot/dts`. A variable without a counterpart in `kernel_build` stops the
build with the next step, and so does a board that sets `BOARD_KERNEL_BUILD_MODULE` together with
`TARGET_KERNEL_SOURCE` or `TARGET_KERNEL_CONFIG`. A user build stops when a board uses a kernel not
built from source.

The module type builds kernels that build with clang and `LLVM=1` and whose host programs build
against the glibc 2.17 sysroot of the platform's prebuilts. Kernels of version 6.11 and newer can
no longer use that sysroot's headers, so 6.11 is the first kernel version not supported by the
module type. It does not build the image targets `Image.lz4` and `Image.lzma`: the build provides no
lz4, lz4c, lzma or lzop.

`kernel_dir` has to be a `git checkout` with at least one commit: the kernel's reported build date
is the date of its last commit, and the build falls back to no other date, so a kernel source
copied without its git directory, or a checkout without a commit, fails the build.

The kernel is built again when any file below `kernel_dir`, `extra_source_dirs` or
`external_modules.root` changes, whether git tracks it or not: an editor's swap file written there
makes the next build run the kernel's rules again, and one written while they run fails that build.

## The userfaultfd garbage collector

The module `uffd_gc_support` sets whether ART uses the userfaultfd GC, from the kernel of the
`kernel_uapi_headers` module named by `kernel_headers`. ART uses it when the `.config` of that
kernel sets `CONFIG_USERFAULTFD=y` and the user-space headers exported by that kernel define
`UFFD_USER_MODE_ONLY`, `UFFD_FEATURE_SIGBUS` and `MREMAP_DONTUNMAP`. ART tests the same facts on the
device in `art/runtime/gc/collector/mark_compact.cc`, and one more: whether `MREMAP_DONTUNMAP` works
on a shared mapping, which neither the configuration nor the headers show. The decision becomes the
dex2oat flag of dexpreopt and `ro.dalvik.vm.enable_uffd_gc` in the product build.prop. Without
`BOARD_KERNEL_BUILD_MODULE`, building dexpreopt or the product build.prop fails with the error set
by `generated_kernel_includes`, since only a kernel built in the tree can give the decision.

## Files chosen by screen density

A `density_prebuilt_etc` module is a `prebuilt_etc` that installs `density_src` from the folder of
the device's density bucket, `mdpi` to `xxxhdpi`. The density comes from `TARGET_SCREEN_DENSITY`,
else `PRODUCT_AAPT_PREF_CONFIG`, else `mdpi`. The module must install to product, vendor or odm.
