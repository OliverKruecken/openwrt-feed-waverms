include $(TOPDIR)/rules.mk

PKG_NAME:=waverms-agent
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

include $(INCLUDE_DIR)/package.mk

define Package/waverms-agent
  SECTION:=utils
  CATEGORY:=Utilities
  TITLE:=WaveRMS Remote Management Agent
  DEPENDS:=+mosquitto-client +jsonfilter +libuci
endef

define Package/waverms-agent/description
  Device agent for the WaveRMS remote management platform.
  Handles bootstrap registration, device info publishing,
  and remote command execution via MQTT over TLS.
endef

define Package/waverms-agent/install
  $(INSTALL_DIR) $(1)/usr/share/waverms/lib
  $(INSTALL_BIN) ./files/usr/share/waverms/agent.sh       $(1)/usr/share/waverms/
  $(INSTALL_BIN) ./files/usr/share/waverms/lib/bootstrap.sh  $(1)/usr/share/waverms/lib/
  $(INSTALL_BIN) ./files/usr/share/waverms/lib/mqtt.sh       $(1)/usr/share/waverms/lib/
  $(INSTALL_BIN) ./files/usr/share/waverms/lib/uci_apply.sh  $(1)/usr/share/waverms/lib/
  $(INSTALL_DIR) $(1)/etc/init.d
  $(INSTALL_BIN) ./files/waverms-agent.init                $(1)/etc/init.d/waverms-agent
endef

$(eval $(call BuildPackage,waverms-agent))