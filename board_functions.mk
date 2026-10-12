# SPDX-FileCopyrightText: The halogenOS Project
# SPDX-License-Identifier: Apache-2.0
#
# Board functions called by the Qualcomm repositories, each written over its AOSP counterpart.
# Each returns true or empty, because the callers compare the result with true.

# $(call is-board-platform-in-list,platforms)
define is-board-platform-in-list
$(if $(call is-board-platform-in-list2,$(1)),true)
endef

# $(call is-vendor-board-platform,QCOM)
# True on a board with BOARD_USES_QCOM_HARDWARE set to true whose platform is listed in
# QCOM_BOARD_PLATFORMS, set by the Qualcomm common board configuration. A board without
# Qualcomm hardware never reaches is-vendor-board-qcom, which stops the build when it finds
# neither variable set.
define is-vendor-board-platform
$(if $(filter-out 1,$(words $(1)))$(filter-out QCOM,$(1)),\
  $(error is-vendor-board-platform: only QCOM is supported, got "$(strip $(1))"; test the board \
  variables of that vendor in the calling makefile instead))$(strip \
$(if $(filter true,$(BOARD_USES_QCOM_HARDWARE)),$(if $(call is-vendor-board-qcom),true)))
endef

# $(call is-platform-sdk-version-at-least,version)
define is-platform-sdk-version-at-least
$(if $(call math_gt_or_eq,$(PLATFORM_SDK_VERSION),$(1)),true)
endef
