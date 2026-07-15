import QtQuick
import QtQuick.Controls
import org.kde.kirigami as Kirigami

Kirigami.FormLayout {
    // cfg_* aliases bind to the matching entries in config/main.xml.
    property alias cfg_agentwatchPath: pathField.text
    property alias cfg_pollIntervalMs: intervalSpin.value

    TextField {
        id: pathField
        Kirigami.FormData.label: i18n("cenci binary path:")
        // Use an absolute path if cenci is not on the Plasma session PATH.
    }

    SpinBox {
        id: intervalSpin
        Kirigami.FormData.label: i18n("Poll interval (ms):")
        from: 250
        to: 60000
        stepSize: 250
    }
}
