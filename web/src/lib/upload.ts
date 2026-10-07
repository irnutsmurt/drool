import { getCsrfToken, ApiError } from "@/api/client";

/** Stream a file with progress (fetch can't report upload progress). */
export function upload(method: "PUT" | "POST", url: string, body: Blob, onProgress?: (fraction: number) => void,
  signal?: AbortSignal): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open(method, "/api/v1" + url);
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    const csrf = getCsrfToken();
    if (csrf) xhr.setRequestHeader("X-DROOL-CSRF", csrf);
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress?.(e.loaded / e.total);
    xhr.onload = () => {
      let data: unknown;
      try { data = JSON.parse(xhr.responseText); } catch { data = undefined; }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data);
      else reject(new ApiError(xhr.status, data as never));
    };
    xhr.onerror = () => reject(new Error("the upload was interrupted"));
    xhr.onabort = () => reject(new Error("upload cancelled"));
    signal?.addEventListener("abort", () => xhr.abort());
    xhr.send(body);
  });
}
