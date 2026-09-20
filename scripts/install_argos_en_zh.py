#!/usr/bin/env python3
"""Download the Argos English-to-Chinese package once for offline use."""
from argostranslate import package

package.update_package_index()
available = package.get_available_packages()
model = next((item for item in available if item.from_code == "en" and item.to_code == "zh"), None)
if model is None:
    raise SystemExit("Argos package index does not contain an en→zh model")
package.install_from_path(model.download())
print("Argos en→zh model installed")
