#!/usr/bin/env python3
"""A small HTTP client for deploy/linux/check_app.sh, run with the payload's
own Python so that a test host needs no curl.

Usage: api.py [-m SECONDS] METHOD URL [JSON-BODY]
Prints the response body; the exit status is 0 for a 2xx answer, 1 for any
other, and 2 when the server cannot be reached.
"""
import sys
import urllib.error
import urllib.request

args = sys.argv[1:]
timeout = 30.0
if args[:1] == ['-m']:
    timeout = float(args[1])
    args = args[2:]
method, url = args[0], args[1]
body = args[2].encode() if len(args) > 2 else None
req = urllib.request.Request(url, data=body, method=method, headers={'content-type': 'application/json'} if body is not None else {})
try:
    with urllib.request.urlopen(req, timeout=timeout) as r:
        sys.stdout.write(r.read().decode())
except urllib.error.HTTPError as e:
    sys.stdout.write(e.read().decode())
    sys.exit(1)
except (urllib.error.URLError, OSError, TimeoutError) as e:
    sys.stderr.write(f'{e}\n')
    sys.exit(2)
