// The pairtask extension's stable path for `pi -e` (#155). It loads the
// extension from the pi bundle the pairtask-pi wrapper resolved, named by
// PAIRTASK_PI_BUNDLE, so Pi and its extension come from one verified bundle.

import { join } from "node:path";
import { pathToFileURL } from "node:url";

const root = process.env.PAIRTASK_PI_BUNDLE;
if (!root) {
	throw new Error("PAIRTASK_PI_BUNDLE is not set: start Pi with `pi` or `pairtask start pi`, which resolve the pi bundle");
}
const extension = await import(pathToFileURL(join(root, "extension", "index.ts")).href);

export default extension.default;
