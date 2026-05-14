include $(TOPDIR)/rules.mk

PKG_NAME:=waverms-agent-go-poc
PKG_VERSION:=0.1.0
PKG_RELEASE:=1

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/OliverKruecken/waverms-agent
GO_PKG_LDFLAGS_X:=main.Version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk
$(call include_mk, golang-package.mk)

define Package/waverms-agent-go-poc
  SECTION:=utils
  CATEGORY:=Utilities
  TITLE:=WaveRMS Agent Go PoC
  DEPENDS:=$(GO_ARCH_DEPENDS)
endef

define Package/waverms-agent-go-poc/description
  Minimal Go binary to validate Go toolchain support
  in the OpenWrt buildroot for the WaveRMS agent.
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) ./go.mod $(PKG_BUILD_DIR)/
	$(CP) ./main.go $(PKG_BUILD_DIR)/
endef

define Package/waverms-agent-go-poc/install
	$(call GoPackage/Package/Install/Bin,$(1))
endef

$(eval $(call GoPackage,waverms-agent-go-poc))
$(eval $(call BuildPackage,waverms-agent-go-poc))