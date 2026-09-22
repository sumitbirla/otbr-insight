#!/usr/bin/env python3
"""Turn a real Matter diagnostics export into one that is safe to publish.

The device report screenshot has to be populated with a real export — the whole
point of the view is what a real file contains — but an export carries the
Thread network's identity, every address the device holds, its certificates and
the controller's node ID. This replaces all of it with the documentation values
the rest of the tree uses, keeping the same lengths and shapes so the decoder
exercises exactly what it would on the original.

What is deliberately kept: vendor and product names, firmware and hardware
versions, counters, and sensor readings. Those are what make the screenshot
worth looking at, and none of them identifies a network or a person.

    python3 tools/screenshots/sanitise.py <export.json> [out.json]
"""

import base64
import json
import sys

# Documentation values, reused from the fixtures elsewhere in the tree.
DOC_NETWORK = "OpenThread-b1a2"
DOC_PANID = 0xB1A2
DOC_XPANID = 0x1122334455667788
DOC_EXTADDR = bytes([0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08])
DOC_MESHLOCAL = bytes([0x40, 0xFD, 0xDE, 0xAD, 0x00, 0xBE, 0xEF, 0x00, 0x00])
DOC_ADDRESSES = [
    "fddead00beef0000000000fffe007000",  # routing locator
    "fd11223344550001 0a1b2c3d4e5f6071".replace(" ", ""),  # off-mesh routable
    "fddead00beef00001a2b3c4d5e6f7081",  # mesh-local EID
    "fe800000000000000302030405060708",  # link local
]


def tlv_certificate(fabric_id, subject_tag, subject_id, key_seed):
    """Build a Matter operational certificate (core spec A.7 / 6.5).

    Real certificates carry a live fabric's root public key, so the screenshot
    gets synthetic ones in the two shapes seen in the wild: with a fabric ID in
    the subject, and without (which is what Home Assistant's own root does).
    """
    buf = bytearray()

    def open_element(kind, tag=None):
        buf.append(kind | (0x20 if tag is not None else 0))
        if tag is not None:
            buf.append(tag)

    def uint64(tag, value):
        open_element(0x07, tag)
        buf.extend(value.to_bytes(8, "little"))

    def octets(tag, value):
        open_element(0x10, tag)
        buf.append(len(value))
        buf.extend(value)

    key = bytes([0x04] + [(i * 7 + key_seed) & 0xFF for i in range(1, 65)])
    open_element(0x15)                       # anonymous struct
    octets(1, bytes([0x01]))                 # serial number
    uint64(4, 662774400)                     # not before, 2021-01-01
    uint64(5, 978134400)                     # not after,  2030-12-30
    open_element(0x17, 6)                    # subject, a list
    uint64(subject_tag, subject_id)
    if fabric_id is not None:
        uint64(21, fabric_id)
    buf.append(0x18)
    octets(9, key)
    buf.append(0x18)
    return base64.b64encode(bytes(buf)).decode(), key


def sanitise(document):
    node = document["data"]["node"]
    attributes = node["attributes"]
    b64 = lambda raw: base64.b64encode(raw).decode()

    # Basic information: the unique ID is per-device, the rest is a product.
    attributes["0/40/18"] = "0102030405060708090a0b0c0d0e0f10"

    # Thread identity and every address the device holds.
    for path in ("0/53/2",):
        if path in attributes:
            attributes[path] = DOC_NETWORK
    if "0/53/3" in attributes:
        attributes["0/53/3"] = DOC_PANID
    if "0/53/4" in attributes:
        attributes["0/53/4"] = DOC_XPANID
    if "0/53/5" in attributes:
        attributes["0/53/5"] = b64(DOC_MESHLOCAL)
    if "0/53/13" in attributes:
        attributes["0/53/13"] = 9
    for entry in attributes.get("0/51/0", []):
        entry["0"] = DOC_NETWORK
        entry["4"] = b64(DOC_EXTADDR)
        entry["6"] = [b64(bytes.fromhex(a)) for a in DOC_ADDRESSES][: len(entry.get("6", []))]

    # Neighbour and route tables: extended addresses are effectively MACs, and
    # an RLOC16 names a specific router on the real mesh.
    for index, entry in enumerate(attributes.get("0/53/7", [])):
        entry["0"] = 0x0A0B0C0D0E0F1011 + index
        entry["2"] = 0x2400
    for index, entry in enumerate(attributes.get("0/53/8", [])):
        entry["0"] = 0x0102030405060708 + index
        entry["1"] = [0x0800, 0x2400][index % 2]
        entry["2"] = [2, 9][index % 2]

    # The provisioned network ID of a Thread network is its extended PAN ID.
    network_id = b64(DOC_XPANID.to_bytes(8, "big"))
    for entry in attributes.get("0/49/1", []):
        entry["0"] = network_id
    if "0/49/6" in attributes:
        attributes["0/49/6"] = network_id

    # Who may administer the device: a real controller's node ID.
    for entry in attributes.get("0/31/0", []):
        entry["3"] = [9988776]

    # Certificates. The roots keep their two distinct shapes; the NOC keeps its
    # pairing with an intermediate CA.
    own_root, own_key = tlv_certificate(None, 20, 1, 11)
    other_root, _ = tlv_certificate(DOC_XPANID, 20, 0x0102030405060708, 23)
    noc, _ = tlv_certificate(1, 17, 0x19, 31)
    icac, _ = tlv_certificate(None, 20, 2, 41)
    if "0/62/4" in attributes:
        attributes["0/62/4"] = [other_root, own_root]
    for entry in attributes.get("0/62/0", []):
        entry["1"], entry["2"] = noc, icac
    for entry in attributes.get("0/62/1", []):
        entry["1"] = base64.b64encode(own_key).decode()

    # The controller states its own compressed fabric ID separately; dropping it
    # leaves the decoder deriving one rather than cross-checking a stale value.
    document["data"]["server_info"].pop("compressed_fabric_id", None)

    # Time zone is a location hint, and the commissioning timestamps say when
    # somebody set their home up.
    document["home_assistant"]["timezone"] = "UTC"
    for entry in attributes.get("0/56/5", []):
        entry["0"], entry["2"] = 0, "UTC"
    node["date_commissioned"] = "2026-01-01T00:00:00.000000"
    node["last_interview"] = "2026-01-01T00:00:00.000000"
    document["setup_times"] = {"null": {"setup": 0.004}}
    return document


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    source = sys.argv[1]
    target = sys.argv[2] if len(sys.argv) > 2 else "tools/screenshots/fixtures/diagnostics-export.json"
    with open(source, encoding="utf-8") as handle:
        document = json.load(handle)
    with open(target, "w", encoding="utf-8") as handle:
        json.dump(sanitise(document), handle, indent=2)
        handle.write("\n")
    print("wrote", target)


if __name__ == "__main__":
    main()
