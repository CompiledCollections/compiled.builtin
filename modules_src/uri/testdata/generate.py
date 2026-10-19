"""Generate python.json, the expected results for uri_test.go, from CPython
and ansible-core's own uri code. Run with the Python that runs Ansible, from
modules_src/uri (file paths in the cases are relative to it):

    python3 testdata/generate.py > testdata/python.json
"""

import email.message
import json
import netrc
import string
import urllib.parse

from ansible.module_utils.urls import mask_url, prepare_multipart
from ansible.modules.uri import form_urlencoded

JSON_VALUES = [
    {"z": 1, "a": [True, False, None], "f": 1.5, "big": 12345678901234567890},
    {"s": "ünïcode ✓ \"quoted\" \\ back", "astral": "𝄞", "ctrl": "\x00\x1f\x7f\t\n\r\b\f", "slash": "/"},
    [1.0, 1e100, -0.0, 0.1, 2.5e-10, -7],
    "just a string",
    None,
    {"nested": {"deep": [{"k": "v"}, []]}, "empty": {}},
]

FORM_BODIES = [
    {"name": "a b&c=d", "unicode": "é✓", "list": [1, "two"], "none": None, "flag": True, "num": 3, "f": 1.5},
    [["a", 1], ["b", [2, 3]], ["c", "x y"], ["d", {"k1": 1, "k2": 2}]],
    {"nested": {"x": 1}, "list_of_lists": [[1, 2]], "safe": "_.-~ +/%"},
    [["a"]],
    [["a", "b", "c"]],
    ["ab"],
    [5],
    {},
]

MULTIPART_FIELDS = [
    {"text": "plain value", "b": {"content": "inline\ncontent\r\nmore", "filename": "inline.txt", "mime_type": "text/plain"}},
    {"guessed": {"content": "{\"k\": 1}", "filename": "data.json"}, "unknown": {"content": "x", "filename": "file.zzz"},
     "nomime": {"content": "no filename"}},
    {"file": {"filename": "testdata/upload.bin"}, "file7": {"filename": "testdata/upload.bin", "multipart_encoding": "7or8bit"}},
    {"quoted": {"content": "q", "filename": "we\"ird\\name.txt"}, "num": {"content": 5, "filename": "n.txt"}},
    {"bad": 5},
    {"bad": {"mime_type": "text/plain"}},
    {"bad": {"filename": "testdata/upload.bin", "multipart_encoding": "base32"}},
    "not a mapping",
]

URLS = [
    "http://example.com/path", "http://user:pass@example.com/x", "http://user@example.com/x",
    "https://user:p%40ss@host:8443/a?b=c#d", "http://user:@example.com/x", "ftp://u:p@h/f",
]

CONTENT_TYPES = [
    "application/json", "application/json; charset=ISO-8859-1", "Text/HTML;Charset=\"utf-16\"",
    "", "json", "text/plain; charset=utf-8, latin-1", "application/vnd.api+json",
    "multipart/form-data; boundary=x; charset=us-ascii",
]

DISPOSITIONS = [
    'attachment; filename="report.pdf"', "attachment; filename=plain.txt", 'inline; filename="dir/sub/x.bin"',
    'attachment; filename="esc\\"aped.txt"', "attachment", "",
]

JOINS = [
    ("http://h/a/b", "c"), ("http://h/a/b", "/c"), ("http://h/a/b", "../c?x=1"), ("http://h/a/b?q", "?r"),
    ("http://h/a/b", "https://other/x"), ("http://h/a/b", "//other/y"), ("http://****:****@h/a", "next"),
]

REDIRECT_QUOTES = ["/path with space", "/ünï", "/ok/!$&'()*+,;=:@", "/%41already"]

NETRC_HOSTS = ["example.com", "other.org", "unknown.net"]


def result(fn, *args):
    try:
        return {"out": fn(*args)}
    except (TypeError, ValueError) as e:
        return {"error": type(e).__name__, "msg": str(e)}
    except OSError as e:
        return {"error": "OSError", "msg": str(e)}


def multipart(fields):
    out = result(prepare_multipart, fields)
    if "out" in out:
        content_type, body = out["out"]
        boundary = content_type.split('boundary="')[1].rstrip('"')
        out["out"] = {
            "content_type": content_type.replace(boundary, "BOUNDARY"),
            "body": body.replace(boundary.encode(), b"BOUNDARY").decode("utf-8", "surrogateescape").encode("utf-8", "surrogateescape").decode("latin-1"),
        }
    return out


def content_type(value):
    m = email.message.Message()
    if value:
        m["Content-Type"] = value
    ct = (m.get_content_type() or "application/octet-stream").split(",")[0]
    return {"content_type": ct, "sub_type": ct.split("/")[1], "charset": (m.get_param("charset") or "utf-8").split(",")[0]}


def disposition(value):
    m = email.message.Message()
    if value:
        m["Content-Disposition"] = value
    name = m.get_param("filename", header="content-disposition")
    return name.split("/")[-1] if name else None


rc = netrc.netrc("testdata/netrc")

print(json.dumps({
    "json": [{"in": json.dumps(v), "out": json.dumps(v)} for v in JSON_VALUES],
    "form": [dict(in_=json.dumps(b), **result(form_urlencoded, b)) for b in FORM_BODIES],
    "multipart": [dict(in_=json.dumps(f), **multipart(f)) for f in MULTIPART_FIELDS],
    "mask_url": [{"in": u, "out": mask_url(u)} for u in URLS],
    "content_type": [dict(in_=v, **content_type(v)) for v in CONTENT_TYPES],
    "disposition": [{"in": v, "out": disposition(v)} for v in DISPOSITIONS],
    "urljoin": [{"base": b, "ref": r, "out": urllib.parse.urljoin(b, r)} for b, r in JOINS],
    "redirect_quote": [{"in": q, "out": urllib.parse.quote(q, encoding="iso-8859-1", safe=string.punctuation)} for q in REDIRECT_QUOTES],
    "netrc": [{"host": h, "out": list(rc.authenticators(h) or [])} for h in NETRC_HOSTS],
}, ensure_ascii=False, indent=1).replace('"in_"', '"in"'))
