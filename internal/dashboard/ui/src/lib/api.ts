// Fetch layer: JSON in/out with the API's {error} envelope surfaced as throws.

export async function fetchJSON<T = any>(path: string): Promise<T> {
  const res = await fetch(path);
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data as T;
}

export async function post(path: string, body: unknown) {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error((await res.json()).error || res.statusText);
}

export async function del(path: string, body: unknown) {
  const res = await fetch(path, {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error((await res.json()).error || res.statusText);
}
