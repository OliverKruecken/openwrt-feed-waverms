include $(TOPDIR)/rules.mk

PKG_NAME:=waverms-agent
PKG_VERSION:=1.8.1
PKG_RELEASE:=1

PKG_SOURCE:=$(PKG_NAME)-$(PKG_VERSION).tar.gz
PKG_SOURCE_URL:=https://codeload.github.com/OliverKruecken/waverms-agent/tar.gz/v$(PKG_VERSION)?
PKG_HASH:=73f6d6b04eadd2571a3c7680666d2005ee4fb1f0ac2b9fadc285fb7340a9688f

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

PKG_LICENSE:=Apache-2.0
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=Oliver Kruecken <okruecken@pdfmodel.org>

GO_PKG:=github.com/OliverKruecken/waverms-agent
GO_PKG_BUILD_PKG:=$(GO_PKG)
GO_PKG_LDFLAGS_X:=main.Version=$(PKG_VERSION)
GO_PKG_LDFLAGS:=-s -w

include $(INCLUDE_DIR)/package.mk
$(call include_mk, golang-package.mk)
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/waverms-agent
  SECTION:=admin
  CATEGORY:=Administration
  TITLE:=WaveRMS Remote Management Agent
  URL:=https://github.com/OliverKruecken/waverms-agent
  DEPENDS:=$(GO_ARCH_DEPENDS) +ca-bundle
endef

define Package/waverms-agent/description
  WaveRMS remote management agent for OpenWrt devices.
  Connects to the WaveRMS server via MQTT, manages device
  configuration via UCI.
endef

define Package/waverms-agent/conffiles
/etc/waverms/config
endef

define Package/waverms-agent/install
	$(call GoPackage/Package/Install/Bin,$(1))
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/waverms-agent.init $(1)/etc/init.d/waverms-agent
	$(INSTALL_DIR) $(1)/etc/udhcpc.user.d
	$(INSTALL_BIN) ./files/60-waverms-bootstrap $(1)/etc/udhcpc.user.d/60-waverms-bootstrap
	$(INSTALL_DIR) $(1)/etc/uci-defaults
	$(INSTALL_BIN) ./files/60-waverms-udhcpc-opts $(1)/etc/uci-defaults/60-waverms-udhcpc-opts
endef

$(eval $(call GoPackage,waverms-agent))
$(eval $(call BuildPackage,waverms-agent))
