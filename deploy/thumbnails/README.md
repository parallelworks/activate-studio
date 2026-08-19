# Workflow thumbnails

Square 512px PNGs with transparent backgrounds, used as the `imageUrl` on
ACTIVATE workflow records and as branding inside a deployment.

- `activate-studio.png` is the Parallel Works mark, cropped from the full
  logo lockup, for the general Studio workflow.
- `cfd-studio.png` and `cfd-studio-dark.png` are the CFD deployment's
  icons, the same pair that ships inside its starter corpus under
  `.branding/`.

A record points at one with a raw file URL on the repository's main
branch. Where a platform cannot reach the public internet, upload the
file to the platform instead and use the blob path it returns.
