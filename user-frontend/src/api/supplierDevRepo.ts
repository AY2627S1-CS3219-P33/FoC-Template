// DEV-ONLY in-memory supplier repository, seeded from the template seed data
// (data/csv/supplier-seed-data.csv). It emulates the Supplier Service contract
// — normalized search, exact type filter, name-ordered cursor pagination, name
// conflict, immutable version IDs on update — so the UI is fully demoable before
// the backend wires its suppliers routes. Never used when the app is not in
// developer mode.

import {
  SupplierError,
  type ListParams,
  type Supplier,
  type SupplierPage,
  type SupplierPatch,
  type SupplierWrite,
} from "./supplier";

const DEFAULT_LIMIT = 25;
const MAX_LIMIT = 100;
const IMG = "https://github.com/CS3219-AY2627S1/FoC-Template/blob/main/data/images";

// [name, type, building, floor, locationDescription, lat, long, open, close, image]
type Seed = [string, string, string, string, string, number, number, string, string, string | null];

const SEED: Seed[] = [
  ["Anna's x Soup Union", "Food", "Central Library", "1", "Next to NUS Co-op", 1.296444, 103.773032, "09:00", "18:00", `${IMG}/ANNA.jpeg`],
  ["NUS Co-op", "Shopping", "Central Library", "1", "Inside the library on the right side", 1.2967866, 103.7732677, "09:00", "16:00", `${IMG}/NUS_COOP.jpeg`],
  ["Printer @ Com 2", "Printing", "Com 2", "1", "Next to LT19", 1.2938347, 103.7744572, "00:00", "23:59", `${IMG}/PRINTER_COM2.jpeg`],
  ["Cool Spot", "Food", "Com2", "1", "Opp LT16", 1.2940156, 103.7738478, "09:00", "21:30", `${IMG}/COOL_SPOT.jpeg`],
  ["InstaChef", "Food", "Terrace", "1", "Next to foyer", 1.2938898, 103.7736305, "00:00", "23:59", `${IMG}/INSTACHEF.jpeg`],
  ["Cafe+ Robot Cafe", "Food/Coffee", "Central Library", "1", "Opp to central library entrance", 1.296444, 103.773032, "00:00", "23:59", `${IMG}/ROBOT_CAFE.jpeg`],
  ["A Hot Hideout", "Food", "Prince George's Park", "2", "Near PGP entrance", 1.2908445, 103.7770891, "11:00", "21:30", null],
  ["Arise and Shine", "Food", "Engineering Block E4", "4", "Near LT6", 1.2991517, 103.769064, "08:00", "18:00", null],
  ["Central Square @ YIH", "Food", "Yusof Ishak House", "1", "Closest to Opp UHC bus stop", 1.2984401, 103.7726256, "08:00", "20:00", null],
  ["Pasta Express", "Food", "Frontier", "1", "Aircon section", 1.2947819, 103.7704435, "09:30", "19:30", null],
  ["TOMORO COFFEE", "Food/Coffee", "Hon Sui Sen Memorial Library", "2", "Inside HSSML", 1.2931259, 103.7719943, "08:15", "18:00", null],
  ["Octobox", "Shopping", "Prince George's Park", "2", "Near PGP entrance", 1.2904347, 103.7787588, "00:00", "23:59", null],
  ["Smooy", "Food", "COM3", "1", "The Terrace @ COM3", 1.2948308, 103.7716305, "11:00", "21:00", null],
  ["Goh Bros E-Print Pte Ltd", "Printing", "Yusof Ishak House", "5", "Take the long staircase up YIH", 1.2984905, 103.7720544, "09:00", "18:00", null],
];

function normalize(value: string): string {
  return value.toLowerCase().replace(/\s+/g, " ").trim();
}

function nowIso(): string {
  return new Date().toISOString();
}

class DevRepo {
  private suppliers: Supplier[] = SEED.map((s) => {
    const at = nowIso();
    return {
      supplierId: crypto.randomUUID(),
      versionId: crypto.randomUUID(),
      name: s[0], type: s[1], building: s[2], floor: s[3], locationDescription: s[4],
      latitude: s[5], longitude: s[6], openingTime: s[7], closingTime: s[8], imageUrl: s[9],
      available: true, createdAt: at, updatedAt: at,
    };
  });

  private sorted(): Supplier[] {
    return [...this.suppliers].sort((a, b) => {
      const byName = normalize(a.name).localeCompare(normalize(b.name));
      return byName !== 0 ? byName : a.supplierId.localeCompare(b.supplierId);
    });
  }

  async list(params: ListParams): Promise<SupplierPage> {
    await tick();
    const limit = Math.min(Math.max(params.limit ?? DEFAULT_LIMIT, 1), MAX_LIMIT);
    const q = params.q ? normalize(params.q) : "";
    const type = params.type ? params.type.toLowerCase() : "";
    let rows = this.sorted().filter((s) => {
      const haystack = normalize(`${s.name} ${s.building} ${s.floor} ${s.locationDescription}`);
      const matchesQ = !q || haystack.includes(q);
      const matchesType = !type || s.type.toLowerCase() === type;
      return matchesQ && matchesType;
    });
    const start = params.cursor ? Number(atobSafe(params.cursor)) || 0 : 0;
    const slice = rows.slice(start, start + limit);
    const next = start + limit < rows.length ? btoaSafe(String(start + limit)) : undefined;
    return { items: slice, nextCursor: next };
  }

  async get(id: string): Promise<Supplier> {
    await tick();
    const found = this.suppliers.find((s) => s.supplierId === id);
    if (!found) throw new SupplierError(404, "SUPPLIER_NOT_FOUND", "Supplier not found.");
    return found;
  }

  async create(body: SupplierWrite): Promise<Supplier> {
    await tick();
    this.assertNameFree(body.name, null);
    const at = nowIso();
    const supplier: Supplier = {
      ...body,
      supplierId: crypto.randomUUID(),
      versionId: crypto.randomUUID(),
      available: true,
      createdAt: at,
      updatedAt: at,
    };
    this.suppliers.push(supplier);
    return supplier;
  }

  async update(id: string, patch: SupplierPatch): Promise<Supplier> {
    await tick();
    const index = this.suppliers.findIndex((s) => s.supplierId === id);
    if (index < 0) throw new SupplierError(404, "SUPPLIER_NOT_FOUND", "Supplier not found.");
    if (patch.name) this.assertNameFree(patch.name, id);
    const updated: Supplier = {
      ...this.suppliers[index],
      ...patch,
      versionId: crypto.randomUUID(), // each update is a new immutable version
      updatedAt: nowIso(),
    };
    this.suppliers[index] = updated;
    return updated;
  }

  async remove(id: string): Promise<void> {
    await tick();
    const index = this.suppliers.findIndex((s) => s.supplierId === id);
    if (index < 0) throw new SupplierError(404, "SUPPLIER_NOT_FOUND", "Supplier not found.");
    this.suppliers.splice(index, 1);
  }

  private assertNameFree(name: string, exceptId: string | null): void {
    const target = normalize(name);
    const clash = this.suppliers.some(
      (s) => s.supplierId !== exceptId && normalize(s.name) === target,
    );
    if (clash) {
      throw new SupplierError(
        409,
        "SUPPLIER_NAME_CONFLICT",
        "Another supplier already uses this name.",
      );
    }
  }
}

function tick(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 150));
}
function btoaSafe(value: string): string {
  try {
    return btoa(value);
  } catch {
    return value;
  }
}
function atobSafe(value: string): string {
  try {
    return atob(value);
  } catch {
    return value;
  }
}

export const devRepo = new DevRepo();
