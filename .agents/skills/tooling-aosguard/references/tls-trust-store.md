# An empty CA trust store, and why it does not look like one

An operator verb that fails with `CERTIFICATE_VERIFY_FAILED` is usually not a
wrong endpoint and not a stale credential. On a host whose Python carries no CA
bundle, it is every HTTPS call, always, and the error text points away from the
cause. This page exists so the next reader spends no time on it.

## The symptom

```
$ aosguard ops netlify site show --site coilysiren-dot-me.netlify.app
netlify: cannot reach https://api.netlify.com/api/v1:
  [SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed:
  unable to get local issuer certificate (_ssl.c:1006)
aosguard: exit status 1
```

The verb, the guardfile pattern, the credential and the API are all fine. Only
trust resolution fails, and it fails closed on every call rather than
intermittently, so the verb is simply unavailable until someone re-derives why.

## The cause

A python.org framework build ships its trust store only after its bundled
`Install Certificates.command` is run. Until then the interpreter has no CA
bundle at all, and a `uv` venv built on top of one inherits the emptiness.

Measured on kais-macbook-pro, 2026-09-07:

```
$ /usr/local/bin/python3 -I -c "import ssl; print(ssl.get_default_verify_paths())"
DefaultVerifyPaths(cafile=None, capath=None,
  openssl_cafile_env='SSL_CERT_FILE',
  openssl_cafile='/Library/Frameworks/Python.framework/Versions/3.11/etc/openssl/cert.pem',
  ...)
```

`cafile=None` means that path does not exist, and
`ssl.create_default_context().get_ca_certs()` returns an empty list. The repo's
own `.venv` reported the same zero. `/etc/ssl/cert.pem` sat readable beside
both the whole time, carrying 128 certificates.

## Why `SSL_CERT_FILE` works as a workaround

aosguard execs `python3 -I`, and `-I` implies `-E`, which drops `PYTHON*`
environment variables. `SSL_CERT_FILE` is an OpenSSL variable rather than a
`PYTHON*` one, so it survives isolated mode and repairs trust from outside.
That makes it a good diagnostic and a bad fix: it repairs one shell, and the
next caller starts from the same place.

## What the code does now

`agentic_os/__init__.py` builds a verifying context, and when the interpreter has
loaded no CA certificates it loads the first readable system bundle from an
ordered candidate list (macOS, Debian/Ubuntu, RHEL/Fedora, SUSE/Alpine, then
the two Homebrew roots). Call it through `shared_ssl_context()`, which caches
the result for the process.

Two properties worth keeping when this code is edited:

* **Verification is never weakened.** An unrepairable context is returned
  as-is, so the call still fails closed. Disabling verification would turn a
  loud local failure into a silent security hole, which is strictly worse than
  the bug. `tests/test_tls_trust_store.py` asserts `verify_mode` and `check_hostname`
  directly for exactly this reason.
* **A healthy interpreter is left alone.** The fallback fires only on an empty
  store, so a host with a real platform trust store keeps it rather than
  silently swapping in whichever file sits on disk first.

When trust cannot be repaired, `trust_diagnosis()` returns a sentence naming
the local cause, so the certificate error stops reading as a bad endpoint.

### Why it lives in `__init__.py` rather than its own module

The aosguard guardfile bundle carries `agentic_os/__init__.py` plus exactly the
modules a guardfile execs, derived by `scripts/guardfile-python-modules.sh` from
argv rather than from imports. A bundled module runs with no package around it,
so a helper in any sibling would be unreachable there, and adding a sibling to
the bundle fails the reverse assertion in `tests/test_guardfile_python_exec.py`,
which holds the bundle to modules a guardfile actually execs.

`__init__.py` is the one file the bundle always ships, so putting the fallback
there is what lets a single copy serve bundled modules, ordinary package code
and `scripts/` alike. Moving it to a tidier module would silently strand the
bundled callers. The alternative is teaching the bundler transitive imports,
which changes what the release embeds and is tracked separately.

## The guardfile carries its own copy

`.umbra/guardfiles/aosguard/netlify_domain_alias.py` duplicates the fallback
rather than importing it. Guardfile scripts are embedded into the aosguard
binary by umbra and materialised into `~/.umbra/cache/specverb/<hash>/` at run
time, then executed with `python3 -I` and no package on the path. There is
nothing for them to import, so the duplication is the design rather than an
oversight. An edit to one belongs in both.

Because the script is embedded at build time, a fix here reaches a host only
when a new aosguard is built and released. Do not sideload a local binary to
get it sooner; see the `kai-brew-release` skill.

## Scope of the defect

Any embedded guardfile script or repo module reaching TLS through a bare
`urllib.request.urlopen` inherits this on the same hosts. A survey on
2026-09-07 found eight such call sites across `agentic_os/`, `scripts/` and the
netlify guardfile, all of them broken on this host and all now routed through
the shared context. A new `urlopen` call should pass
`context=shared_ssl_context()` rather than relying on the interpreter default.

Whether a given host's `python3` should be the python.org build at all is a
host-convergence question, not a tooling one, and is deliberately not answered
here.

Filed as `teable:coilyco-flight-deck/agentic-os#7076`.
