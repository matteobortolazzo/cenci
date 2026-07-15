import QtQuick
import QtQuick.Layouts
import Quickshell
import qs.Commons
import qs.Services.UI
import qs.Widgets

Item {
  id: root

  property var pluginApi: null
  property ShellScreen screen
  property string widgetId: ""
  property string section: ""
  property int sectionWidgetIndex: -1
  property int sectionWidgetsCount: 0
  property bool hovered: false

  readonly property string screenName: screen ? screen.name : ""
  readonly property string barPosition: Settings.getBarPositionForScreen(screenName)
  readonly property bool isVertical: barPosition === "left" || barPosition === "right"
  readonly property real barHeight: Style.getBarHeightForScreen(screenName)
  readonly property real capsuleHeight: Style.getCapsuleHeightForScreen(screenName)
  readonly property real barFontSize: Style.getBarFontSizeForScreen(screenName)

  readonly property string cssClass: pluginApi?.mainInstance?.cssClass || "none"
  readonly property string text: pluginApi?.mainInstance?.text || ""
  readonly property bool isVisible: pluginApi?.mainInstance?.hasOutput || false
  readonly property var headroom: pluginApi?.mainInstance?.headroom || ({})
  readonly property var sortedHeadroomKeys: Object.keys(headroom).sort()

  visible: isVisible
  opacity: isVisible ? 1.0 : 0.0

  function iconForClass(c) {
    switch (c) {
      case "failed":     return "alert-octagon"
      case "need-input": return "alert-triangle"
      case "running":    return "robot"
      case "done":       return "circle-check"
      case "stopped":    return "player-pause"
      case "idle":       return "robot-face"
      default:           return "robot-face"
    }
  }

  function colorForClass(c) {
    switch (c) {
      case "failed":     return Color.mError
      case "need-input": return Color.mError
      case "running":    return Color.mPrimary
      case "done":       return Color.mTertiary
      case "stopped":    return Color.mSecondary
      default:           return Color.mOnSurface
    }
  }

  // Round-half-up percent from a 0.0-1.0 fraction (e.g. 0.156 -> 16).
  function percentFor(frac) {
    return Math.floor(frac * 100 + 0.5)
  }

  // Same thresholds as headroomClass in status.go:179 and colorForHeadroom
  // in plugin/macos/cenci.5s.sh:125 — keep all three in sync.
  function classForHeadroom(pct) {
    if (pct > 25) return "normal"
    if (pct >= 10) return "warning"
    return "critical"
  }

  function colorForHeadroom(pct) {
    switch (classForHeadroom(pct)) {
      case "normal":  return Color.mPrimary
      case "warning": return Color.mTertiary
      default:        return Color.mError
    }
  }

  readonly property real contentWidth: isVertical ? capsuleHeight : layout.implicitWidth + Style.marginS * 2
  readonly property real contentHeight: isVertical ? layout.implicitHeight + Style.marginS * 2 : capsuleHeight

  implicitWidth: contentWidth
  implicitHeight: contentHeight

  Rectangle {
    id: visualCapsule
    x: Style.pixelAlignCenter(parent.width, width)
    y: Style.pixelAlignCenter(parent.height, height)
    width: root.contentWidth
    height: root.contentHeight
    color: root.hovered ? Color.mHover : Style.capsuleColor
    radius: Style.radiusM
    border.color: Style.capsuleBorderColor
    border.width: Style.capsuleBorderWidth

    Item {
      id: layout
      anchors.centerIn: parent

      implicitWidth: grid.implicitWidth
      implicitHeight: grid.implicitHeight

      GridLayout {
        id: grid
        columns: root.isVertical ? 1 : 2
        rowSpacing: Style.marginS
        columnSpacing: Style.marginS

        NIcon {
          Layout.alignment: Qt.AlignHCenter | Qt.AlignVCenter
          icon: root.iconForClass(root.cssClass)
          color: root.hovered ? Color.mOnHover : root.colorForClass(root.cssClass)
        }

        RowLayout {
          Layout.alignment: Qt.AlignHCenter | Qt.AlignVCenter
          spacing: Style.marginS

          NText {
            Layout.alignment: Qt.AlignHCenter | Qt.AlignVCenter
            text: root.text
            color: root.hovered ? Color.mOnHover : Color.mOnSurface
            pointSize: root.barFontSize
          }

          Repeater {
            model: root.sortedHeadroomKeys

            delegate: Rectangle {
              id: badge
              required property string modelData
              readonly property int pct: root.percentFor(root.headroom[modelData])

              Layout.alignment: Qt.AlignHCenter | Qt.AlignVCenter
              radius: height / 2
              color: "transparent"
              border.width: 1
              border.color: root.hovered ? Color.mOnHover : root.colorForHeadroom(pct)
              implicitWidth: badgeText.implicitWidth + Style.marginS * 2
              implicitHeight: badgeText.implicitHeight + Style.marginS

              NText {
                id: badgeText
                anchors.centerIn: parent
                text: badge.pct + "%"
                color: root.hovered ? Color.mOnHover : root.colorForHeadroom(badge.pct)
                pointSize: root.barFontSize
              }
            }
          }
        }
      }
    }
  }

  MouseArea {
    anchors.fill: parent
    hoverEnabled: true
    acceptedButtons: Qt.RightButton
    cursorShape: Qt.ArrowCursor

    onEntered: {
      root.hovered = true
      const tip = pluginApi?.mainInstance?.tooltip || ""
      if (tip.length > 0) {
        TooltipService.show(root, tip, BarService.getTooltipDirection(root.screenName))
      }
    }

    onExited: {
      root.hovered = false
      TooltipService.hide()
    }

    onPressed: mouse => {
      if (mouse.button === Qt.RightButton) {
        TooltipService.hide()
        PanelService.showContextMenu(contextMenu, root, screen)
      }
    }

    NPopupContextMenu {
      id: contextMenu

      model: [
        {
          "label": "Widget settings",
          "action": "widget-settings",
          "icon": "settings"
        }
      ]

      onTriggered: action => {
        contextMenu.close()
        PanelService.closeContextMenu(screen)
        if (action === "widget-settings") {
          BarService.openPluginSettings(screen, pluginApi.manifest)
        }
      }
    }
  }
}
