#!/usr/bin/env python3
"""Generate a minimal Windows amd64 .syso containing RT_ICON/RT_GROUP_ICON
and, with --version, an RT_VERSION block (file/product version, company,
description) shown in the exe's Properties > Details tab.
No external Python packages are required. Input must be an .ico file.
"""
from __future__ import annotations
import argparse, struct
from dataclasses import dataclass
from pathlib import Path

RT_ICON = 3
RT_GROUP_ICON = 14
RT_VERSION = 16
LANG_EN_US = 0x0409
MACHINE_AMD64 = 0x8664
REL_AMD64_ADDR32NB = 0x0003
SECTION_CHARACTERISTICS = 0xC0300040  # initialized data, 4-byte align, read/write


def align4(n: int) -> int:
    return (n + 3) & ~3

@dataclass
class IcoEntry:
    width: int
    height: int
    colors: int
    reserved: int
    planes: int
    bit_count: int
    size: int
    offset: int
    data: bytes

@dataclass
class Leaf:
    data: bytes
    data_entry_off: int = 0
    raw_off: int = 0

class DirNode:
    def __init__(self, entries):
        self.entries = entries  # list[(id, DirNode|Leaf)]
        self.off = 0


def parse_ico(path: Path):
    b = path.read_bytes()
    if len(b) < 6:
        raise ValueError('ICO file too short')
    reserved, kind, count = struct.unpack_from('<HHH', b, 0)
    if reserved != 0 or kind != 1 or count < 1:
        raise ValueError('Not a Windows icon (.ico) file')
    entries=[]
    for i in range(count):
        off=6+i*16
        if off+16 > len(b):
            raise ValueError('Truncated ICO directory')
        w,h,c,r,planes,bpp,size,img_off=struct.unpack_from('<BBBBHHII',b,off)
        if img_off+size > len(b):
            raise ValueError('Truncated ICO image')
        entries.append(IcoEntry(w,h,c,r,planes,bpp,size,img_off,b[img_off:img_off+size]))
    return entries


def make_group(entries, icon_ids):
    out=bytearray(struct.pack('<HHH',0,1,len(entries)))
    for e, rid in zip(entries, icon_ids):
        out += struct.pack('<BBBBHHIH',e.width,e.height,e.colors,e.reserved,e.planes,e.bit_count,e.size,rid)
    return bytes(out)


def _pad4(b: bytearray):
    while len(b) % 4:
        b.append(0)


def _ver_block(key: str, value: bytes = b'', text: bool = False, children=()) -> bytes:
    """One VS_VERSIONINFO-style node: wLength, wValueLength, wType, key, value, children."""
    b = bytearray(6)
    b += (key + '\0').encode('utf-16-le')
    _pad4(b)
    b += value
    for c in children:
        _pad4(b)
        b += c
    struct.pack_into('<HHH', b, 0, len(b), len(value) // 2 if text else len(value), 1 if text else 0)
    return bytes(b)


def make_version_info(version: str, strings: dict) -> bytes:
    nums = [int(x) for x in version.split('.')] + [0, 0, 0, 0]
    ms = (nums[0] << 16) | nums[1]
    ls = (nums[2] << 16) | nums[3]
    fixed = struct.pack('<13I', 0xFEEF04BD, 0x00010000, ms, ls, ms, ls,
                        0x3F, 0, 0x00040004, 1, 0, 0, 0)  # VOS_NT_WINDOWS32, VFT_APP
    table = _ver_block('040904B0', text=True, children=[
        _ver_block(k, (v + '\0').encode('utf-16-le'), text=True) for k, v in strings.items()])
    sfi = _ver_block('StringFileInfo', text=True, children=[table])
    vfi = _ver_block('VarFileInfo', text=True, children=[
        _ver_block('Translation', struct.pack('<HH', LANG_EN_US, 1200))])
    return _ver_block('VS_VERSION_INFO', fixed, children=[sfi, vfi])


def make_resource_section(ico_entries, version_info: bytes | None = None):
    icon_ids=list(range(2,2+len(ico_entries)))
    icon_names=[]
    for rid,e in zip(icon_ids,ico_entries):
        icon_names.append((rid,DirNode([(LANG_EN_US,Leaf(e.data))])))
    group=make_group(ico_entries,icon_ids)
    root=DirNode([
        (RT_ICON,DirNode(icon_names)),
        (RT_GROUP_ICON,DirNode([(1,DirNode([(LANG_EN_US,Leaf(group))]))])),
    ])
    if version_info:
        root.entries.append((RT_VERSION,DirNode([(1,DirNode([(LANG_EN_US,Leaf(version_info))]))])))

    dirs=[]; leaves=[]; cursor=0
    def assign_dirs(node):
        nonlocal cursor
        node.entries.sort(key=lambda kv: kv[0])
        node.off=cursor
        dirs.append(node)
        cursor += 16 + 8*len(node.entries)
        for _,child in node.entries:
            if isinstance(child,DirNode):
                assign_dirs(child)
            else:
                leaves.append(child)
    assign_dirs(root)

    # One IMAGE_RESOURCE_DATA_ENTRY per leaf.
    for leaf in leaves:
        leaf.data_entry_off=cursor
        cursor += 16
    cursor=align4(cursor)
    for leaf in leaves:
        leaf.raw_off=cursor
        cursor += len(leaf.data)
        cursor=align4(cursor)

    section=bytearray(cursor)
    reloc_offsets=[]
    for node in dirs:
        struct.pack_into('<IIHHHH',section,node.off,0,0,0,0,0,len(node.entries))
        ent_off=node.off+16
        for rid,child in node.entries:
            if isinstance(child,DirNode):
                target=0x80000000 | child.off
            else:
                target=child.data_entry_off
            struct.pack_into('<II',section,ent_off,rid,target)
            ent_off += 8
    for leaf in leaves:
        struct.pack_into('<IIII',section,leaf.data_entry_off,leaf.raw_off,len(leaf.data),0,0)
        reloc_offsets.append(leaf.data_entry_off)
        section[leaf.raw_off:leaf.raw_off+len(leaf.data)]=leaf.data
    return bytes(section), reloc_offsets


def write_syso(out_path: Path, section: bytes, reloc_offsets):
    raw_ptr=20+40
    reloc_ptr=raw_ptr+len(section)
    sym_ptr=reloc_ptr+10*len(reloc_offsets)
    header=struct.pack('<HHIIIHH',MACHINE_AMD64,1,0,sym_ptr,1,0,0x0004)
    name=b'.rsrc\0\0\0'
    section_header=struct.pack('<8sIIIIIIHHI',name,0,0,len(section),raw_ptr,reloc_ptr,0,len(reloc_offsets),0,SECTION_CHARACTERISTICS)
    relocs=b''.join(struct.pack('<IIH',off,0,REL_AMD64_ADDR32NB) for off in reloc_offsets)
    symbol=struct.pack('<8sIhHBB',name,0,1,0,3,0)
    string_table=struct.pack('<I',4)
    out_path.write_bytes(header+section_header+section+relocs+symbol+string_table)


def main():
    ap=argparse.ArgumentParser()
    ap.add_argument('ico')
    ap.add_argument('out')
    ap.add_argument('--version', help='e.g. 4.0.11; adds an RT_VERSION resource')
    args=ap.parse_args()
    entries=parse_ico(Path(args.ico))
    vi=None
    if args.version:
        full='.'.join((args.version.split('.')+['0','0','0','0'])[:4])
        vi=make_version_info(args.version, {
            'CompanyName': 'AIXAI tools',
            'FileDescription': 'AIXAI All-in-One Downloader',
            'FileVersion': full,
            'InternalName': 'AIXAI_AllInOne_Downloader',
            'LegalCopyright': 'Copyright (c) 2026 AIXAI tools. MIT License.',
            'OriginalFilename': 'AIXAI_AllInOne_Downloader_Windows_x64.exe',
            'ProductName': 'AIXAI All-in-One Downloader',
            'ProductVersion': args.version,
        })
    section,relocs=make_resource_section(entries, vi)
    write_syso(Path(args.out),section,relocs)
    print(f'embedded {len(entries)} icon layers; {len(section)} resource bytes; {len(relocs)} relocations')

if __name__=='__main__':
    main()
