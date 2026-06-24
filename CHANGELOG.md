# Changelog

## [0.2.1](https://github.com/home-operations/chaski/compare/0.2.0...0.2.1) (2026-06-24)


### Features

* **container:** update image mirror.gcr.io/curlimages/curl (8.20.0 → 8.21.0) ([#30](https://github.com/home-operations/chaski/issues/30)) ([8abb241](https://github.com/home-operations/chaski/commit/8abb241867a2664a01d74244b7a0fcbb53c0dd6b))
* **server:** log monitoring-endpoint requests at debug ([#26](https://github.com/home-operations/chaski/issues/26)) ([f0eba28](https://github.com/home-operations/chaski/commit/f0eba285ea33d4ba2199f4cd61df1152abe1de46))


### Miscellaneous Chores

* add minimumGroupSize to Go toolchain configuration ([53a15d2](https://github.com/home-operations/chaski/commit/53a15d23ac0f1130419090ab3463783b89700009))
* **mise:** Update tool oxfmt (0.55.0 → 0.56.0) ([#28](https://github.com/home-operations/chaski/issues/28)) ([775fba5](https://github.com/home-operations/chaski/commit/775fba50e4b0d54e0a64c5bfef777ac707eab262))

## [0.2.0](https://github.com/home-operations/chaski/compare/0.1.2...0.2.0) (2026-06-21)


### ⚠ BREAKING CHANGES

* **config:** model verify as a github|hmac|token union ([#23](https://github.com/home-operations/chaski/issues/23))
* **server:** serve webhooks at /hooks/{route} ([#21](https://github.com/home-operations/chaski/issues/21))

### Features

* **config:** model verify as a github|hmac|token union ([#23](https://github.com/home-operations/chaski/issues/23)) ([0d30d44](https://github.com/home-operations/chaski/commit/0d30d44f51aecfb71d27c6ef28ff249bb700656c))
* **config:** self-documenting schema + route/target descriptions ([#22](https://github.com/home-operations/chaski/issues/22)) ([6552a7c](https://github.com/home-operations/chaski/commit/6552a7c00cb37aa82a0981819a7e4874c8c8eb38))
* **server:** serve webhooks at /hooks/{route} ([#21](https://github.com/home-operations/chaski/issues/21)) ([eb72be9](https://github.com/home-operations/chaski/commit/eb72be9c6a8921106b071b71309ae80687aa7f2f))


### Bug Fixes

* **config:** tighten route target validation ([#19](https://github.com/home-operations/chaski/issues/19)) ([6f31e6a](https://github.com/home-operations/chaski/commit/6f31e6a682649ccae7829787b3a62ab1126d9955))

## [0.1.2](https://github.com/home-operations/chaski/compare/0.1.1...0.1.2) (2026-06-21)


### Features

* **relay:** per-target whenExpr for conditional fan-out ([#18](https://github.com/home-operations/chaski/issues/18)) ([4dca3fd](https://github.com/home-operations/chaski/commit/4dca3fd4b25ddb5b8184e817e7464acd2715bec0))


### Bug Fixes

* **render:** make the snippet reference walk fail-closed ([#16](https://github.com/home-operations/chaski/issues/16)) ([4ac4f10](https://github.com/home-operations/chaski/commit/4ac4f10fc879615ab84a955d2451b0da269597b9))
* **render:** validate the snippet reference graph at load ([#14](https://github.com/home-operations/chaski/issues/14)) ([5ee0d20](https://github.com/home-operations/chaski/commit/5ee0d205cce7ada04f01aa0d8ba43d68bfb79eba))


### Documentation

* right-size the CEL surface in the README ([#17](https://github.com/home-operations/chaski/issues/17)) ([8a5c016](https://github.com/home-operations/chaski/commit/8a5c016440d0dd25feae678f3d53cf18e5bf3b68))

## [0.1.1](https://github.com/home-operations/chaski/compare/0.1.0...0.1.1) (2026-06-21)


### Features

* **metrics:** per-target, webhook-reject, retry, and build-info metrics ([#10](https://github.com/home-operations/chaski/issues/10)) ([c01980f](https://github.com/home-operations/chaski/commit/c01980fdde2e1c96e3c450df1a0cfecc993d1669))
* **relay:** make the why-didn't-it-fire loop answerable ([#8](https://github.com/home-operations/chaski/issues/8)) ([dceca16](https://github.com/home-operations/chaski/commit/dceca16ca2a4013a4110739e12cd06188538bb41))
* **render:** shared named templates ([#12](https://github.com/home-operations/chaski/issues/12)) ([a8cd9a1](https://github.com/home-operations/chaski/commit/a8cd9a1b31e7d68793c7b4c0f850f77d9cac49b0))
* **smtp:** add optional SMTP ingestion listener ([#5](https://github.com/home-operations/chaski/issues/5)) ([20cdf43](https://github.com/home-operations/chaski/commit/20cdf439a5834eed46d155623281d67e4bbf834b))
* **validate:** render a route against a sample payload offline ([#9](https://github.com/home-operations/chaski/issues/9)) ([ea3254d](https://github.com/home-operations/chaski/commit/ea3254d6c4048624728758f405fc1b1f0c603f7e))


### Bug Fixes

* **chart:** make the NOTES quickstart curl runnable as printed ([#7](https://github.com/home-operations/chaski/issues/7)) ([4ec70f9](https://github.com/home-operations/chaski/commit/4ec70f9bb1e69dac79e959ff93a6179d4a02e3ae))
* **deps:** update module go.yaml.in/yaml/v4 (v4.0.0-rc.5 → v4.0.0-rc.6) ([#2](https://github.com/home-operations/chaski/issues/2)) ([ffecb78](https://github.com/home-operations/chaski/commit/ffecb7825b96e2a0aec7a84ea23bbde1ddeb7d0a))
* **deps:** update module google.golang.org/protobuf (v1.36.10 → v1.36.11) ([#3](https://github.com/home-operations/chaski/issues/3)) ([e1ca9a2](https://github.com/home-operations/chaski/commit/e1ca9a2e0d842437ed10c54602876242330d373e))
* **render:** isolate field templates from snippet namespace ([#13](https://github.com/home-operations/chaski/issues/13)) ([288dec6](https://github.com/home-operations/chaski/commit/288dec68011abb1f35b92d4293ed5cea089322a7))
* **smtp:** correct shutdown drain order and close review gaps ([#6](https://github.com/home-operations/chaski/issues/6)) ([02171fe](https://github.com/home-operations/chaski/commit/02171fe89959175029d1fd6b6d007e64a9d685a6))


### Miscellaneous Chores

* **mise:** Update tool zizmor (1.25.2 → 1.26.1) ([#11](https://github.com/home-operations/chaski/issues/11)) ([1ec0a11](https://github.com/home-operations/chaski/commit/1ec0a11c6db962b9447fe70e22fb423274081573))

## 0.1.0 (2026-06-20)


### Features

* chaski — a stateless, CEL-gated webhook relay ([c18f986](https://github.com/home-operations/chaski/commit/c18f986720e11fbf54376a6cde93f82e3c1dddd0))

## Changelog
