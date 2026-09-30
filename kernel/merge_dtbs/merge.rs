// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! The merges of a base device tree: how overlays split them and apply to them, in which order
//! the overlays apply, and the names the merges are written under.

use std::ffi::{OsStr, OsString};
use std::os::unix::ffi::{OsStrExt, OsStringExt};
use std::path::{Path, PathBuf};

use crate::ids::TreeIds;
use crate::programs::Program;

/// The two formats of device tree files.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DeviceTreeFormat {
    /// A device tree blob, `.dtb`.
    Dtb,
    /// A device tree overlay blob, `.dtbo`.
    Dtbo,
}

impl DeviceTreeFormat {
    const ALL: [Self; 2] = [Self::Dtb, Self::Dtbo];

    /// The file name extension, without its dot.
    fn extension(self) -> &'static str {
        match self {
            Self::Dtb => "dtb",
            Self::Dtbo => "dtbo",
        }
    }

    /// The format of a path's file name extension; None for another extension and for none.
    fn of_path(path: &Path) -> Option<Self> {
        let extension = path.extension()?;
        Self::ALL.into_iter().find(|format| extension == OsStr::new(format.extension()))
    }

    /// The program that applies overlays to a base of this format.
    pub fn overlay_program(self) -> Program {
        match self {
            Self::Dtb => Program::Fdtoverlay,
            Self::Dtbo => Program::Fdtoverlaymerge,
        }
    }
}

/// The path of a device tree file, with the format its file name extension gives.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DeviceTreePath {
    path: PathBuf,
    format: DeviceTreeFormat,
}

impl DeviceTreePath {
    /// The path of a device tree file; None when the file name has no device tree extension. The
    /// extension follows the last dot of the name, which is not its first character.
    pub fn new(path: PathBuf) -> Option<Self> {
        let format = DeviceTreeFormat::of_path(&path)?;
        Some(Self { path, format })
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    pub fn format(&self) -> DeviceTreeFormat {
        self.format
    }

    /// The file's name.
    fn name(&self) -> &OsStr {
        self.path.file_name().expect("a path with an extension has a file name")
    }

    /// The parts of the file's name before its extension, split at every `-`.
    fn stem_parts(&self) -> Vec<&[u8]> {
        let stem = self.path.file_stem().expect("a path with an extension has a file name");
        stem.as_bytes().split(|&byte| byte == b'-').collect()
    }
}

/// A device tree file and the ids of its root node.
#[derive(Debug)]
pub struct TreeFile {
    file: DeviceTreePath,
    ids: TreeIds,
}

impl TreeFile {
    pub fn new(file: DeviceTreePath, ids: TreeIds) -> Self {
        Self { file, ids }
    }

    pub fn path(&self) -> &Path {
        self.file.path()
    }

    pub fn format(&self) -> DeviceTreeFormat {
        self.file.format()
    }

    pub fn ids(&self) -> &TreeIds {
        &self.ids
    }
}

/// One merge of a base: its ids, and the overlays applied to it in the order of the walk.
#[derive(Debug)]
pub struct Merge<'a> {
    pub base: &'a TreeFile,
    pub ids: TreeIds,
    pub applied: Vec<&'a TreeFile>,
}

impl<'a> Merge<'a> {
    /// The merge a base starts with: the base's ids, and no overlay.
    pub fn new(base: &'a TreeFile) -> Self {
        Self { base, ids: base.ids().clone(), applied: Vec::new() }
    }

    /// A piece of this merge with other ids and the overlays applied so far.
    fn piece(&self, ids: TreeIds) -> Self {
        Self { base: self.base, ids, applied: self.applied.clone() }
    }

    /// The order in which the overlays apply, the last one winning where two set the same
    /// property: an overlay whose ids are a proper subset of another's applies after it, and
    /// overlays that neither contains keep the order of the walk. Each step takes the first
    /// overlay in walk order that is more specific than none of the overlays left.
    pub fn application_order(&self) -> Vec<&'a TreeFile> {
        let mut left = self.applied.clone();
        let mut order = Vec::with_capacity(left.len());
        while !left.is_empty() {
            let next = left
                .iter()
                .position(|candidate| {
                    !left.iter().any(|other| {
                        other.path() != candidate.path()
                            && candidate.ids().is_more_specific_than(other.ids())
                    })
                })
                .expect("proper subsets form a strict order, so an overlay left is most general");
            order.push(left.remove(next));
        }
        order
    }

    /// The name the merge is written under: the base's name for the one merge of a base, and
    /// otherwise the base's parts, the parts of the applied overlays in walk order that are none
    /// of the base's, and the hex of the hash of the ids, joined with `-`, and the base's
    /// extension.
    fn written_name(&self, single: bool) -> OsString {
        if single {
            return self.base.file.name().to_owned();
        }
        let hash = python_hex(self.ids.name_hash());
        let base_parts = self.base.file.stem_parts();
        let overlay_parts = self.applied.iter().flat_map(|overlay| overlay.file.stem_parts());
        let mut parts = base_parts.clone();
        parts.extend(overlay_parts.filter(|part| !base_parts.contains(part)));
        parts.push(hash.as_bytes());
        let mut name = OsString::from_vec(parts.join(&b'-'));
        name.push(".");
        name.push(self.base.format().extension());
        name
    }

    /// The file the merge is written to in a directory, see `written_name`; its format is the
    /// base's.
    pub fn written_file(&self, directory: &Path, single: bool) -> DeviceTreePath {
        let path = directory.join(self.written_name(single));
        DeviceTreePath { path, format: self.base.format() }
    }
}

/// Python's `hex()` of a value: lowercase digits after `0x`, and `-0x` for a negative value.
fn python_hex(value: i64) -> String {
    let sign = if value < 0 { "-" } else { "" };
    format!("{sign}0x{:x}", value.unsigned_abs())
}

/// Splits and applies one overlay on the merges of one base; whether it applied to any of them.
///
/// No two merges of the list have equal ids. A merge the overlay fits that splits for it leaves
/// the list, and its pieces are appended, each unless a merge with equal ids is in the list at
/// that moment. Then the overlay is added to every merge it fits.
pub fn apply_overlay<'a>(merges: &mut Vec<Merge<'a>>, overlay: &'a TreeFile) -> bool {
    // The merges present before this overlay are visited once each, in order; they stay at the
    // front, and the pieces are appended after them.
    let mut position = 0;
    for _ in 0..merges.len() {
        let ids = &merges[position].ids;
        match overlay.ids().overlap(ids).and_then(|overlap| ids.split(&overlap)) {
            None => position += 1,
            Some(pieces) => {
                let merge = merges.remove(position);
                for ids in pieces {
                    if !merges.iter().any(|other| other.ids == ids) {
                        merges.push(merge.piece(ids));
                    }
                }
            }
        }
    }
    let mut applied = false;
    for merge in merges.iter_mut().filter(|merge| overlay.ids().fits(&merge.ids)) {
        merge.applied.push(overlay);
        applied = true;
    }
    applied
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ids::ids;
    use crate::text::shown_text;

    fn file(path: &str, ids: TreeIds) -> TreeFile {
        TreeFile::new(DeviceTreePath::new(path.into()).unwrap(), ids)
    }

    fn applied_names(merge: &Merge) -> Vec<String> {
        merge.applied.iter().map(|overlay| shown_text(overlay.path())).collect()
    }

    #[test]
    fn formats() {
        let of = |path: &str| DeviceTreePath::new(path.into()).map(|file| file.format());
        assert_eq!(of("a.dtb"), Some(DeviceTreeFormat::Dtb));
        assert_eq!(of("dir.d/a.b.dtbo"), Some(DeviceTreeFormat::Dtbo));
        // The extension is matched exactly, after the last dot, which may not start the name.
        for path in [".dtb", "a.DTB", "a.dtb.orig", "adtb", "a.dtbx", "a", "a.dtb/..", "dtb"] {
            assert_eq!(of(path), None, "{path}");
        }
        assert_eq!(of("..dtb"), Some(DeviceTreeFormat::Dtb));
    }

    #[test]
    fn split_keeps_existing() {
        let base = file("base.dtb", TreeIds::default());
        let first = file("first.dtbo", TreeIds::default());
        let second = file("second.dtbo", ids().platform(&[2, 0]));
        let existing = Merge {
            base: &base,
            ids: ids().platform(&[2, 0]).board(&[8, 0]),
            applied: vec![&first],
        };
        let wide = Merge {
            base: &base,
            ids: ids().platform(&[1, 0, 2, 0]).board(&[8, 0]),
            applied: vec![],
        };
        let mut merges = vec![existing, wide];
        assert!(apply_overlay(&mut merges, &second));
        // Piece a of the wide merge equals the existing merge and does not enter the list; the
        // existing merge keeps its applied overlays and gets the new one.
        let merge_ids: Vec<&TreeIds> = merges.iter().map(|merge| &merge.ids).collect();
        assert_eq!(
            merge_ids,
            [&ids().platform(&[2, 0]).board(&[8, 0]), &ids().platform(&[1, 0]).board(&[8, 0])]
        );
        assert_eq!(applied_names(&merges[0]), ["first.dtbo", "second.dtbo"]);
        assert!(merges[1].applied.is_empty());
    }

    #[test]
    fn apply_after_split() {
        let base = file("base.dtb", ids().platform(&[1, 0, 2, 0]).board(&[8, 0]));
        let a = file("a.dtbo", ids().platform(&[2, 0]));
        let b = file("b.dtbo", ids().board(&[8, 0]));
        let c = file("c.dtbo", ids().board(&[9, 0]));
        let mut merges = vec![Merge::new(&base)];
        assert!(apply_overlay(&mut merges, &a));
        let merge_ids: Vec<&TreeIds> = merges.iter().map(|merge| &merge.ids).collect();
        assert_eq!(
            merge_ids,
            [&ids().platform(&[2, 0]).board(&[8, 0]), &ids().platform(&[1, 0]).board(&[8, 0])]
        );
        assert_eq!(applied_names(&merges[0]), ["a.dtbo"]);
        assert!(merges[1].applied.is_empty());
        // An overlay that fits both pieces is added to both, after the earlier one.
        assert!(apply_overlay(&mut merges, &b));
        assert_eq!(applied_names(&merges[0]), ["a.dtbo", "b.dtbo"]);
        assert_eq!(applied_names(&merges[1]), ["b.dtbo"]);
        // An overlay that fits no merge applies to none.
        assert!(!apply_overlay(&mut merges, &c));
        assert_eq!(applied_names(&merges[0]), ["a.dtbo", "b.dtbo"]);
        assert_eq!(applied_names(&merges[1]), ["b.dtbo"]);
    }

    #[test]
    fn pieces_keep_the_overlays_applied_before_the_split() {
        let base = file("base.dtb", ids().platform(&[1, 0, 2, 0]));
        let a = file("a.dtbo", ids().platform(&[1, 0, 2, 0]));
        let b = file("b.dtbo", ids().platform(&[2, 0]));
        let mut merges = vec![Merge::new(&base)];
        // a fits the whole merge, which does not split for it.
        assert!(apply_overlay(&mut merges, &a));
        assert_eq!(merges.len(), 1);
        // b splits the merge: both pieces keep a, before b.
        assert!(apply_overlay(&mut merges, &b));
        let merge_ids: Vec<&TreeIds> = merges.iter().map(|merge| &merge.ids).collect();
        assert_eq!(merge_ids, [&ids().platform(&[2, 0]), &ids().platform(&[1, 0])]);
        assert_eq!(applied_names(&merges[0]), ["a.dtbo", "b.dtbo"]);
        assert_eq!(applied_names(&merges[1]), ["a.dtbo"]);
    }

    #[test]
    fn split_merges_stay_in_front_of_new_pieces() {
        let base = file("base.dtb", TreeIds::default());
        let overlay = file("o.dtbo", ids().platform(&[2, 0]));
        let kept = Merge { base: &base, ids: ids().platform(&[3, 0]), applied: vec![] };
        let split = Merge { base: &base, ids: ids().platform(&[1, 0, 2, 0]), applied: vec![] };
        let last = Merge { base: &base, ids: ids().platform(&[4, 0]), applied: vec![] };
        let mut merges = vec![split, kept, last];
        assert!(apply_overlay(&mut merges, &overlay));
        let merge_ids: Vec<&TreeIds> = merges.iter().map(|merge| &merge.ids).collect();
        assert_eq!(
            merge_ids,
            [
                &ids().platform(&[3, 0]),
                &ids().platform(&[4, 0]),
                &ids().platform(&[2, 0]),
                &ids().platform(&[1, 0]),
            ]
        );
    }

    #[test]
    fn application_order() {
        let base = file("base.dtb", TreeIds::default());
        let a = file("a.dtbo", ids().platform(&[3, 0]));
        let b = file("b.dtbo", ids().platform(&[1, 0, 2, 0, 3, 0]));
        let c = file("c.dtbo", ids().board(&[8, 0]));
        let d = file("d.dtbo", ids().platform(&[1, 0, 2, 0, 3, 0]));
        let e = file("e.dtbo", ids().platform(&[2, 0, 3, 0]));
        let f = file("f.dtbo", ids().platform(&[3, 0]).pmic(&[1, 2, 3, 4]));
        let g = file("g.dtbo", ids().platform(&[3, 0, 4, 0]));
        let order = |applied: Vec<&TreeFile>| -> Vec<String> {
            let merge = Merge { base: &base, ids: TreeIds::default(), applied };
            merge.application_order().iter().map(|overlay| shown_text(overlay.path())).collect()
        };
        // The specific overlay comes first in walk order and applies after the general one; the
        // overlay with other ids keeps its place in walk order.
        assert_eq!(order(vec![&a, &b, &c]), ["b.dtbo", "a.dtbo", "c.dtbo"]);
        assert_eq!(order(vec![&c, &a, &b]), ["c.dtbo", "b.dtbo", "a.dtbo"]);
        // Equal ids are neither more specific: walk order stays.
        assert_eq!(order(vec![&d, &b]), ["d.dtbo", "b.dtbo"]);
        assert_eq!(order(vec![&b, &d]), ["b.dtbo", "d.dtbo"]);
        // A chain: each overlay applies after every overlay whose ids contain its own.
        assert_eq!(order(vec![&a, &e, &b]), ["b.dtbo", "e.dtbo", "a.dtbo"]);
        // An overlay that lacks a kind names every id of it, so it is the more general one.
        assert_eq!(order(vec![&f, &a]), ["a.dtbo", "f.dtbo"]);
        // Overlays whose ids overlap without one containing the other keep walk order.
        assert_eq!(order(vec![&g, &e]), ["g.dtbo", "e.dtbo"]);
    }

    #[test]
    fn single_merge_name() {
        let base = file("dts/vendor/waipio-mtp.dtb", ids().platform(&[1, 0]));
        let overlay = file("dts/audio/waipio-audio.dtbo", TreeIds::default());
        let merge = Merge { base: &base, ids: base.ids().clone(), applied: vec![&overlay] };
        assert_eq!(merge.written_name(true), "waipio-mtp.dtb");
    }

    #[test]
    fn split_name() {
        let base = file("dts/vendor/waipio-mtp-overlay.dtbo", TreeIds::default());
        let overlays = [
            file("dts/audio/waipio-audio-overlay.dtbo", TreeIds::default()),
            file("dts/camera/waipio-camera-sensor-mtp.dtbo", TreeIds::default()),
            file("dts/display/waipio-sde-sensor.dtbo", TreeIds::default()),
        ];
        let merge = Merge {
            base: &base,
            ids: ids().platform(&[1, 0]).board(&[8, 0]).pmic(&[1, 2, 3, 4]),
            applied: overlays.iter().collect(),
        };
        // The base's parts, then each overlay's parts that are none of the base's; sensor, which
        // two overlays share, is there twice. The hash of these ids is negative.
        assert_eq!(
            merge.written_name(false),
            "waipio-mtp-overlay-audio-camera-sensor-sde-sensor--0x7b256b06da7e2eb5.dtbo"
        );
    }

    #[test]
    fn hash_names() {
        let base = file("base.dtb", TreeIds::default());
        let over = file("over.dtbo", TreeIds::default());
        let name =
            |ids: TreeIds| Merge { base: &base, ids, applied: vec![&over] }.written_name(false);
        assert_eq!(name(ids().platform(&[1, 0])), "base-over--0x2e89c762fe3aa26c.dtb");
        assert_eq!(name(ids().platform(&[2, 0])), "base-over-0x843b331131325f0.dtb");
    }
}
