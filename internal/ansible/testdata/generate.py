"""Generate python.json, the expected results for compat_test.go, from CPython.

Run with the Python Ansible uses, from this directory:

    python3 generate.py > python.json
"""

import datetime
import fnmatch
import json
import os
import re
import shlex
import time
import warnings

warnings.simplefilter('ignore', FutureWarning)  # Python warns about [[ in classes

SHLEX = [
    "echo hello", "a 'b c' \"d e\"", "x\\ y", '"a\\"b"', '"a\\nb"', "'a\\nb'", '""', "a '' b",
    "  lead trail  ", "tab\tsep\nnl", 'mix"ed"\'quo\'tes', "esc\\\\aped", '"\\\\"', "a\\", "'open",
    '"open', "", "é ü", "a#b c",
]
QUOTE = ["", "abc", "a b", "it's", "--x=1", "@%+=:,./-_", "é", "$HOME"]
ENV = {"HOME": "/home/u", "FOO": "foo", "EMPTY": "", "A_1": "a1"}
EXPANDVARS = [
    "$FOO", "${FOO}", "$FOO/bar", "${FOO}bar", "$FOObar", "$NOPE", "${NOPE}", "${", "${FOO", "$", "$$FOO",
    "a$EMPTY.b", "${}", "$A_1x", "$A_1", "é$FOO", "${FOO}${FOO}",
]
EXPANDUSER = ["~", "~/x", "~//x", "x~", "~nosuchuser_xyz/x", "~root", "~root/x"]
TIMEDELTA_US = [0, 1, 999999, 1000000, 61500000, 3600000000, 86400000000, 90061000001, 2 * 86400000000 + 5]
DATETIMES = [(2024, 1, 2, 3, 4, 5, 0), (2024, 12, 31, 23, 59, 59, 123456), (2024, 6, 1, 0, 0, 0, 1)]
REGEX = [
    (r"^key=", "key=1\n"), (r"^b=\d$", "b=2\n"), (r"2\Z", "b=2\n"), (r"2\Z", "b=2"), (r"^\w+=", "näme=1\n"),
    (r"^\d$", "٣\n"), (r"a\sb", "a\x0bb\n"), (r"a\sb", "a b\n"), (r"^\S\W\S$", "x-y\n"), (r"(?i)^key", "KEY\n"),
    (r"^(?i:key)=", "KEY=\n"), (r"^a{,2}$", "aa\n"), (r"^a{,2}$", "aaa\n"), (r"café", "café\n"),
    (r"a\011b", "a\tb\n"), (r"a(?#c)b", "ab\n"), (r"^[[:a]$", ":\n"), (r"^[[:a]$", "a\n"), (r"x$", "x\n\n"),
    (r"(?m)x$", "x\ny\n"), (r"[\d]+", "٣٤\n"), (r"[\w-]+$", "é-x\n"), (r"\x41", "A\n"), (r"[]a]", "]\n"),
    (r"[^]a]", "]\n"), (r"\.", "a.b\n"), (r"(?s)a.b", "a\nb"), (r"\Aab", "ab\n"), (r"a\Z", "a"), (r"^$", "\n"),
    (r"^$", ""), (r"\bfoo\b", "a foo b\n"), (r"(?a)\w+$", "abc\n"), (r"(?a)^\w$", "é\n"), (r"[\s]", "\x0b"),
    (r"\0", "\x00"), (r"\101", "A"), (r"^(\w+)=(\d*)$", "k=12\n"), (r"(?P<k>\w)(?P<v>\d)?", "a\n"),
]
EXPAND = [
    (r"^(\w+)=(.*)$", "name=old value", r"\1=new (\2)"),
    (r"^(?P<key>\w+)=(?P<val>.*)$", "name=old", r"\g<val>=\g<key> \g<0>"),
    (r"^(\w) (\w)$", "a b", r"\1\t\2\\"),
    (r"^(a)?(b)$", "b", r"x\1y\2"),
    (r"(a)", "a", r"\0\101\x"),
    (r"(a)(b)(c)(d)(e)(f)(g)(h)(i)(j)(k)", "abcdefghijk", r"\11|\1\1|\g<11>"),
    (r"(a)", "a", r"\n\r\a\f\v\b"),
    (r"(a)", "a", r"\q"),
    (r"(a)", "a", r"\5"),
    (r"(a)", "a", r"\g<nope>"),
    (r"(a)", "a", "\\"),
    (r"(a)", "a", r"\-\.\é"),
]
FNMATCH = [
    ("*.txt", "a.txt"), ("*.txt", "a.txtx"), ("?b", "ab"), ("[a-c]x", "bx"), ("[a-c]x", "dx"), ("[!a-c]x", "dx"),
    ("[!a-c]x", "ax"), ("[^a]x", "^x"), ("[^a]x", "bx"), ("a\\b", "a\\b"), ("[]]", "]"), ("[!]]", "a"),
    ("[z-a]", "m"), ("[a-]", "-"), ("*", "a/b"), ("[", "["), ("**", "abc"), ("a[!]", "a!"), ("a[]", "a[]"),
]

STRPTIME_TZ = "America/Mexico_City"
STRPTIME = [
    ("202103040506.07", "%Y%m%d%H%M.%S"), ("2021-03-04 05:06:07", "%Y-%m-%d %H:%M:%S"),
    ("Thu Mar 4 2021 05:06 PM", "%a %b %d %Y %I:%M %p"), ("21-063", "%y-%j"), ("69-1-1", "%y-%m-%d"),
    ("68-1-1", "%y-%m-%d"), ("2021-07-01", "%Y-%m-%d"), ("12:30:15.5", "%H:%M:%S.%f"),
    ("JANUARY 5 2020", "%B %d %Y"), ("2020  1  2", "%Y %m %d"), ("100%", "%d%%"),
    ("yesterday", "%Y%m%d%H%M.%S"), ("202103040506.07xyz", "%Y%m%d%H%M.%S"), ("2021", "%Q"),
    ("2021-13-01", "%Y-%m-%d"), ("2021-02-30", "%Y-%m-%d"),
]
FLOATS = [0.0, 1.0, -2.5, 1609459200.0, 1791348442.9856722, 1e-05, 0.0001, 1e16, 1.5e16, 123456789012345.6, 0.1 + 0.2]
PERCENT = ["check %s", "%s", "a %% b %s", "no arg", "%s %s", "%d", "trail %"]


def strptime_case(value, fmt):
    try:
        return {"in": value, "fmt": fmt, "out": time.mktime(time.strptime(value, fmt))}
    except ValueError as e:
        return {"in": value, "fmt": fmt, "err": str(e)}


def percent_case(fmt):
    try:
        return {"in": fmt, "out": fmt % "X"}
    except (TypeError, ValueError) as e:
        return {"in": fmt, "err": str(e)}


def shlex_case(s):
    try:
        return {"in": s, "out": shlex.split(s)}
    except ValueError as e:
        return {"in": s, "err": str(e)}


def regex_case(pattern, s):
    m = re.search(pattern, s)
    return {"re": pattern, "s": s, "match": m is not None, "groups": list(m.groups()) if m else None}


def expand_case(pattern, s, template):
    try:
        return {"re": pattern, "s": s, "t": template, "out": re.search(pattern, s).expand(template)}
    except (re.error, IndexError) as e:
        return {"re": pattern, "s": s, "t": template, "err": str(e)}


os.environ.clear()
os.environ.update(ENV, TZ=STRPTIME_TZ)
time.tzset()
print(json.dumps({
    "shlex": [shlex_case(s) for s in SHLEX],
    "quote": [{"in": s, "out": shlex.quote(s)} for s in QUOTE],
    "env": ENV,
    "expandvars": [{"in": s, "out": os.path.expandvars(s)} for s in EXPANDVARS],
    "expanduser": [{"in": s, "out": os.path.expanduser(s)} for s in EXPANDUSER],
    "timedelta": [{"us": us, "out": str(datetime.timedelta(microseconds=us))} for us in TIMEDELTA_US],
    "datetime": [{"in": list(d), "out": str(datetime.datetime(*d))} for d in DATETIMES],
    "regex": [regex_case(p, s) for p, s in REGEX],
    "expand": [expand_case(p, s, t) for p, s, t in EXPAND],
    "fnmatch": [{"p": p, "s": s, "out": fnmatch.fnmatchcase(s, p)} for p, s in FNMATCH],
    "strptime_tz": STRPTIME_TZ,
    "strptime": [strptime_case(v, f) for v, f in STRPTIME],
    "pyfloat": [{"in": f, "out": repr(f)} for f in FLOATS],
    "percent": [percent_case(f) for f in PERCENT],
}, ensure_ascii=False, indent=1))
