include $(TOPDIR)/rules.mk

PKG_NAME:=waverms-agent
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

include $(INCLUDE_DIR)/package.mk

define Package/waverms-agent
  SECTION:=utils
  CATEGORY:=Utilities
  TITLE:=WaveRMS Remote Management Agent
  DEPENDS:=+lua +luasocket +luci-lib-uci +luci-lib-jsonc +mosquitto-client
endef

define Package/waverms-agent/description
  WaveRMS remote management agent for OpenWrt devices.
  Connects to the WaveRMS server via MQTT and executes
  configuration commands via UCI.
endef

define Package/waverms-agent/conffiles
/etc/waverms/config
endef

define Package/waverms-agent/install
  $(INSTALL_DIR) $(1)/usr/share/waverms/lib
  $(INSTALL_BIN)  ./files/usr/share/waverms/agent.lua        $(1)/usr/share/waverms/
  $(INSTALL_DATA) ./files/usr/share/waverms/lib/*.lua        $(1)/usr/share/waverms/lib/
  $(INSTALL_DIR)  $(1)/etc/init.d
  $(INSTALL_BIN)  ./files/waverms-agent.init                  $(1)/etc/init.d/waverms-agent
endef

$(eval $(call BuildPackage,waverms-agent))
