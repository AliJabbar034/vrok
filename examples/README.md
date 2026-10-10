# Examples

Each script is runnable and explains what to look for. They use the binary in
`../bin`, so run `make build` first.

| Script               | Shows                                                   |
| -------------------- | ------------------------------------------------------- |
| `share-file.sh`      | A single file, with Range support and a download limit  |
| `share-directory.sh` | A built artefact served like the real thing             |
| `share-multiple.sh`  | Several files behind one generated index                |
| `share-localhost.sh` | A local HTTP server, reverse-proxied                    |
| `share-protected.sh` | A password-protected share, unlocked with curl          |
| `receive-files.sh`   | `vrok receive`, driven with curl as the upload page is  |
| `manage-shares.sh`   | `vrok list`, `revoke` and `stop --all` across processes |
| `run-relay.sh`       | A relay and an agent, end to end on one machine         |

They assert as well as demonstrate: `share-directory.sh` fails loudly if a
traversal attempt ever returns file content, `share-protected.sh` checks that
the unlock cookie contains no part of the password, `receive-files.sh` checks
that a sender cannot write outside the folder or replace a file in it, and
`run-relay.sh` compares the bytes that crossed the tunnel against the original.
