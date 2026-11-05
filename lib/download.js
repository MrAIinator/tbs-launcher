import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

export function sha1File(file) {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash("sha1");
    fs.createReadStream(file)
      .on("error", reject)
      .pipe(hash)
      .on("error", reject)
      .on("finish", () => resolve(hash.digest("hex")));
  });
}

function readWithStall(reader, ms) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("скачивание зависло")), ms);
    reader.read().then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

export async function downloadFile(url, dest, options = {}) {
  const { sha1, onProgress, retries = 4, isCancelled, timeoutMs = 90000 } = options;
  if (!/^https:\/\//i.test(url)) throw new Error("нужна https-ссылка");
  await fs.promises.mkdir(path.dirname(dest), { recursive: true });
  if (sha1 && fs.existsSync(dest) && (await sha1File(dest)) === sha1) return;
  if (!sha1 && fs.existsSync(dest) && fs.statSync(dest).size > 0) return;

  let lastError = null;
  for (let attempt = 1; attempt <= retries; attempt += 1) {
    if (isCancelled?.()) throw new Error("отменено");
    const part = `${dest}.part`;
    try {
      const response = await fetch(url, {
        redirect: "follow",
        headers: { "User-Agent": "TBSLauncher/1.0" },
      });
      if (!response.ok || !response.body) throw new Error(`HTTP ${response.status} для ${path.basename(dest)}`);
      const total = Number(response.headers.get("content-length") || 0);
      const file = fs.createWriteStream(part);
      const reader = response.body.getReader();
      let received = 0;
      while (true) {
        if (isCancelled?.()) {
          await reader.cancel().catch(() => {});
          throw new Error("отменено");
        }
        const { done, value } = await readWithStall(reader, timeoutMs);
        if (done) break;
        received += value.byteLength;
        if (!file.write(Buffer.from(value))) {
          await new Promise((resolve) => file.once("drain", resolve));
        }
        onProgress?.({ received, total, name: path.basename(dest) });
      }
      await new Promise((resolve, reject) => file.end((error) => (error ? reject(error) : resolve())));
      if (sha1 && (await sha1File(part)) !== sha1) throw new Error(`битый файл ${path.basename(dest)}`);
      fs.renameSync(part, dest);
      return;
    } catch (error) {
      lastError = error;
      fs.rmSync(part, { force: true });
      if (String(error?.message || error).includes("отменено") || attempt === retries) break;
      await new Promise((resolve) => setTimeout(resolve, 700 * attempt));
    }
  }
  throw lastError instanceof Error ? lastError : new Error(String(lastError));
}
