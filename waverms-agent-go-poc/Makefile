include $(TOPDIR)/rules.mk

PKG_NAME:=waverms-agent-go-poc
PKG_VERSION:=0.1.0
PKG_RELEASE:=1

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_USE_MIPS16:=0

GO_PKG:=github.com/OliverKruecken/waverms-agent
GO_PKG_LDFLAGS_X:=main.Version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk
include $(GO_INCLUDE_DIR)/golang-package.mk

define Package/waverms-agent-go-poc
  SECTION:=utils
  CATEGORY:=Utilities
  TITLE:=WaveRMS Agent Go PoC
  DEPENDS:=$(GO_ARCH_DEPENDS)
endef