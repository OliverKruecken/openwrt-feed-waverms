include $(TOPDIR)/rules.mk

PKG_NAME:=waverms-agent
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)
PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/OliverKruecken/waverms-agent
GO_PKG_BUILD_PKG:=$(GO_PKG)
GO_PKG_LDFLAGS_X:=main.Version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk
$(call include_mk, golang-package.mk)
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/waverms-agent
  SECTION:=utils
  CATEGORY:=Utilities
  TITLE:=WaveRMS Remote Management Agent
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

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) $(CURDIR)/go.mod $(PKG_BUILD_DIR)/
	$(CP) $(CURDIR)/go.sum $(PKG_BUILD_DIR)/
	$(CP) $(CURDIR)/main.go $(PKG_BUILD_DIR)/
	$(CP) $(CURDIR)/internal $(PKG_BUILD_DIR)/
endef

define Package/waverms-agent/install
	$(call GoPackage/Package/Install/Bin,$(1))
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/waverms-agent.init $(1)/etc/init.d/waverms-agent
	$(INSTALL_DIR) $(1)/etc/udhcpc.d
	$(INSTALL_BIN) ./files/60-waverms-bootstrap $(1)/etc/udhcpc.d/60-waverms-bootstrap
	$(INSTALL_DIR) $(1)/etc/waverms
	$(INSTALL_CONF) ./files/etc/waverms/config $(1)/etc/waverms/config
endef

$(eval $(call GoPackage,waverms-agent))
$(eval $(call BuildPackage,waverms-agent))
