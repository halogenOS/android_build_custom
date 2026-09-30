// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! The platform, board and pmic ids of a device tree file's root node, and the rules that compare
//! them.

use std::num::NonZeroUsize;
use std::path::Path;

use crate::error::MergeError;
use crate::group_set::{Group, GroupSet};
use crate::hash;

/// The three id kinds, in their order.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum IdKind {
    Platform,
    Board,
    Pmic,
}

impl IdKind {
    /// The kinds in their order.
    pub const ALL: [Self; 3] = [Self::Platform, Self::Board, Self::Pmic];

    /// The root node property of the kind.
    pub fn property(self) -> &'static str {
        match self {
            Self::Platform => "qcom,msm-id",
            Self::Board => "qcom,board-id",
            Self::Pmic => "qcom,pmic-id",
        }
    }

    /// The number of values of a group of the kind, given the pmic group size.
    fn group_size(self, pmic_size: NonZeroUsize) -> NonZeroUsize {
        match self {
            Self::Platform | Self::Board => PAIR_SIZE,
            Self::Pmic => pmic_size,
        }
    }
}

/// One optional value for each id kind.
#[derive(Clone, Debug, PartialEq)]
pub struct PerKind<T>([Option<T>; 3]);

impl<T> Default for PerKind<T> {
    fn default() -> Self {
        Self([None, None, None])
    }
}

impl<T> PerKind<T> {
    /// The value of each kind, made in the kinds' order.
    pub fn from_fn(mut value_of: impl FnMut(IdKind) -> Option<T>) -> Self {
        let mut values = Self::default();
        for kind in IdKind::ALL {
            values.set(kind, value_of(kind));
        }
        values
    }

    /// The value of each kind, made in the kinds' order up to the first error.
    pub fn try_from_fn<E>(
        mut value_of: impl FnMut(IdKind) -> Result<Option<T>, E>,
    ) -> Result<Self, E> {
        let mut values = Self::default();
        for kind in IdKind::ALL {
            values.set(kind, value_of(kind)?);
        }
        Ok(values)
    }

    /// The value of a kind.
    pub fn get(&self, kind: IdKind) -> Option<&T> {
        self.0[kind as usize].as_ref()
    }

    /// Sets the value of a kind.
    pub fn set(&mut self, kind: IdKind, value: Option<T>) {
        self.0[kind as usize] = value;
    }
}

/// The property that sets the number of values of a pmic group.
pub const PMIC_SIZE_PROPERTY: &str = "qcom,pmic-id-size";

/// The number of values of a pmic group without `qcom,pmic-id-size`.
const DEFAULT_PMIC_SIZE: NonZeroUsize = NonZeroUsize::new(4).unwrap();

/// The number of values of a platform or board group.
const PAIR_SIZE: NonZeroUsize = NonZeroUsize::new(2).unwrap();

/// Id properties that no input of the kernels merge_dtbs is built for has. A file with one fails,
/// so that no merge ignores such an id.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum UnsupportedProperty {
    QcomOem,
    OplusHw,
    OplusProject,
    XiaomiMiboard,
}

impl UnsupportedProperty {
    /// The properties in the order they are looked up.
    pub const ALL: [Self; 4] =
        [Self::QcomOem, Self::OplusHw, Self::OplusProject, Self::XiaomiMiboard];

    /// The root node property.
    pub fn property(self) -> &'static str {
        match self {
            Self::QcomOem => "qcom,oem-id",
            Self::OplusHw => "oplus,hw-id",
            Self::OplusProject => "oplus,project-id",
            Self::XiaomiMiboard => "xiaomi,miboard-id",
        }
    }
}

/// The id properties of a root node, as fdtget shows them.
#[derive(Debug, Default)]
pub struct RootProperties {
    /// The values of each kind's property, None when the root node lacks it.
    pub ids: PerKind<Vec<i64>>,
    /// The values of `qcom,pmic-id-size`, None when the root node lacks it.
    pub pmic_size: Option<Vec<i64>>,
    /// The first unsupported property the root node has.
    pub unsupported: Option<UnsupportedProperty>,
}

/// The ids of one device tree file: the groups of each kind, None for a kind whose property is
/// absent.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TreeIds(PerKind<GroupSet>);

/// A kind in which a merge splits for an overlay: the groups the overlay covers, and the rest.
struct Differing {
    narrowed: GroupSet,
    remaining: GroupSet,
}

/// One of the two parts of a differing kind's groups.
#[derive(Clone, Copy)]
enum Part {
    /// The groups the overlay covers.
    Narrowed,
    /// The groups the overlay does not cover.
    Remaining,
}

impl Differing {
    fn groups(&self, part: Part) -> &GroupSet {
        match part {
            Part::Narrowed => &self.narrowed,
            Part::Remaining => &self.remaining,
        }
    }
}

impl TreeIds {
    /// The ids of a file from the id properties of its root node.
    pub fn from_properties(path: &Path, properties: &RootProperties) -> Result<Self, MergeError> {
        if let Some(property) = properties.unsupported {
            return Err(MergeError::UnsupportedProperty { path: path.to_owned(), property });
        }
        let pmic_size = match &properties.pmic_size {
            None => DEFAULT_PMIC_SIZE,
            Some(values) => match values.as_slice() {
                [size] => usize::try_from(*size).ok().and_then(NonZeroUsize::new),
                _ => None,
            }
            .ok_or_else(|| MergeError::InvalidPmicSize {
                path: path.to_owned(),
                values: values.clone(),
            })?,
        };
        let groups = PerKind::try_from_fn(|kind| {
            let size = kind.group_size(pmic_size);
            properties
                .ids
                .get(kind)
                .map(|values| group_values(path, kind, values, size))
                .transpose()
        })?;
        Ok(Self(groups))
    }

    /// The groups of a kind.
    pub fn kind(&self, kind: IdKind) -> Option<&GroupSet> {
        self.0.get(kind)
    }

    /// Whether a kind is present, which means it has a group.
    pub fn has_group(&self) -> bool {
        IdKind::ALL.into_iter().any(|kind| self.kind(kind).is_some())
    }

    /// The overlap of these overlay ids with a merge's: per kind, None when either lacks the
    /// kind, and otherwise the overlay's groups that the merge has too. None when the overlay does
    /// not fit the merge. An overlay fits a merge when a kind is present in both and every kind
    /// present in both shares a group.
    pub fn overlap(&self, merge: &TreeIds) -> Option<TreeIds> {
        /// A kind present in both that shares no group.
        struct NoSharedGroup;
        let overlap = PerKind::try_from_fn(|kind| match (self.kind(kind), merge.kind(kind)) {
            (Some(overlay), Some(merged)) => {
                overlay.intersection(merged).map(Some).ok_or(NoSharedGroup)
            }
            _ => Ok(None),
        });
        let overlap = TreeIds(overlap.ok()?);
        overlap.has_group().then_some(overlap)
    }

    /// Whether these overlay ids fit a merge's, see `overlap`.
    pub fn fits(&self, merge: &TreeIds) -> bool {
        self.overlap(merge).is_some()
    }

    /// The pieces a merge with these ids splits into for its overlap with an overlay, or None when
    /// no kind differs.
    ///
    /// A kind differs when the overlap has it and these ids have groups the overlap lacks. The
    /// pieces are, without repetition: a, each differing kind set to the overlap's groups; b, each
    /// differing kind set to the remaining groups; c, when the platform or the board kind differs,
    /// those of the two that differ set as in b and the pmic kind kept; d, when the platform kind
    /// differs, only the platform kind set as in b.
    pub fn split(&self, overlap: &TreeIds) -> Option<Vec<TreeIds>> {
        let differing = PerKind::from_fn(|kind| {
            let narrowed = overlap.kind(kind)?;
            let remaining = self.kind(kind)?.difference(narrowed)?;
            Some(Differing { narrowed: narrowed.clone(), remaining })
        });
        let differs = |kind| differing.get(kind).is_some();
        if !IdKind::ALL.into_iter().any(differs) {
            return None;
        }
        let mut pieces = vec![
            self.replaced(&differing, &IdKind::ALL, Part::Narrowed),
            self.replaced(&differing, &IdKind::ALL, Part::Remaining),
        ];
        if differs(IdKind::Platform) || differs(IdKind::Board) {
            pieces.push(self.replaced(
                &differing,
                &[IdKind::Platform, IdKind::Board],
                Part::Remaining,
            ));
        }
        if differs(IdKind::Platform) {
            pieces.push(self.replaced(&differing, &[IdKind::Platform], Part::Remaining));
        }
        let mut unique: Vec<TreeIds> = Vec::with_capacity(pieces.len());
        for piece in pieces {
            if !unique.contains(&piece) {
                unique.push(piece);
            }
        }
        Some(unique)
    }

    /// These ids with each of the given kinds that differs set to a part of its differing groups.
    fn replaced(&self, differing: &PerKind<Differing>, kinds: &[IdKind], part: Part) -> TreeIds {
        TreeIds(PerKind::from_fn(|kind| {
            let replacement = differing.get(kind).filter(|_| kinds.contains(&kind));
            let replacement = replacement.map(|differing| differing.groups(part));
            replacement.or(self.kind(kind)).cloned()
        }))
    }

    /// Whether the general overlay ids cover every id these overlay ids cover: for each kind, the
    /// general ids lack it, or these ids have it with groups the general ids have. Ids that lack a
    /// kind cover every id of that kind.
    fn is_covered_by(&self, general: &TreeIds) -> bool {
        IdKind::ALL.into_iter().all(|kind| match (general.kind(kind), self.kind(kind)) {
            (None, _) => true,
            (Some(general), Some(specific)) => specific.is_subset(general),
            (Some(_), None) => false,
        })
    }

    /// Whether these overlay ids cover a proper subset of the ids the other overlay ids cover.
    pub fn is_more_specific_than(&self, other: &TreeIds) -> bool {
        self.is_covered_by(other) && !other.is_covered_by(self)
    }

    /// Whether an overlay with these ids may apply to a tree, the pairing of the check: the tree
    /// has a group, and for each kind either lacks it, or the tree's one group is all zeros, or
    /// the two share a group.
    pub fn may_apply_to(&self, tree: &TreeIds) -> bool {
        tree.has_group()
            && IdKind::ALL.into_iter().all(|kind| match (tree.kind(kind), self.kind(kind)) {
                (Some(tree_groups), Some(overlay_groups)) => {
                    let only_zeros = tree_groups.len() == 1
                        && tree_groups.iter().flat_map(Group::values).all(|&value| value == 0);
                    only_zeros || tree_groups.intersects(overlay_groups)
                }
                _ => true,
            })
    }

    /// CPython's hash of the tuple of the kinds, None for an absent kind; the suffix of the name
    /// of a split merge.
    pub fn name_hash(&self) -> i64 {
        let kinds = IdKind::ALL.map(|kind| self.kind(kind).map_or(hash::NONE, GroupSet::hash));
        hash::tuple(&kinds).cast_signed()
    }
}

/// The groups of an id property's values, `size` values each.
fn group_values(
    path: &Path,
    kind: IdKind,
    values: &[i64],
    size: NonZeroUsize,
) -> Result<GroupSet, MergeError> {
    if values.len() % size != 0 {
        return Err(MergeError::IncompleteGroup {
            path: path.to_owned(),
            kind,
            count: values.len(),
            size: size.get(),
        });
    }
    GroupSet::from_groups(values.chunks(size.get()).filter_map(Group::new))
        .ok_or_else(|| MergeError::EmptyProperty { path: path.to_owned(), kind })
}

/// No ids, to build test ids from.
#[cfg(test)]
pub fn ids() -> TreeIds {
    TreeIds::default()
}

#[cfg(test)]
impl TreeIds {
    /// These ids with the platform groups of these values, two to a group; no values make the
    /// kind absent.
    pub fn platform(self, values: &[i64]) -> Self {
        self.with_values(IdKind::Platform, values, 2)
    }

    /// These ids with the board groups of these values, two to a group.
    pub fn board(self, values: &[i64]) -> Self {
        self.with_values(IdKind::Board, values, 2)
    }

    /// These ids with the pmic groups of these values, four to a group.
    pub fn pmic(self, values: &[i64]) -> Self {
        self.with_values(IdKind::Pmic, values, 4)
    }

    fn with_values(mut self, kind: IdKind, values: &[i64], size: usize) -> Self {
        self.0.set(kind, GroupSet::from_groups(values.chunks(size).filter_map(Group::new)));
        self
    }
}

#[cfg(test)]
mod tests {
    use super::ids;
    use super::*;

    /// Root node properties with the values of these kinds and the pmic group size.
    fn properties(kinds: &[(IdKind, &[i64])], pmic_size: Option<&[i64]>) -> RootProperties {
        let mut properties = RootProperties::default();
        for &(kind, values) in kinds {
            properties.ids.set(kind, Some(values.to_vec()));
        }
        properties.pmic_size = pmic_size.map(<[i64]>::to_vec);
        properties
    }

    #[test]
    fn per_kind() {
        let values = PerKind::from_fn(|kind| (kind != IdKind::Board).then_some(kind.property()));
        let shown = IdKind::ALL.map(|kind| values.get(kind).copied());
        assert_eq!(shown, [Some("qcom,msm-id"), None, Some("qcom,pmic-id")]);
        let mut visited = Vec::new();
        let failed = PerKind::<()>::try_from_fn(|kind| {
            visited.push(kind);
            if kind == IdKind::Board {
                Err(kind)
            } else {
                Ok(None)
            }
        });
        assert_eq!(failed, Err(IdKind::Board));
        assert_eq!(visited, [IdKind::Platform, IdKind::Board]);
    }

    #[test]
    fn fit() {
        let merge = ids().platform(&[1, 0, 2, 0]).board(&[8, 0]);
        // A kind present in both shares a group.
        assert!(ids().platform(&[2, 0]).fits(&merge));
        assert!(ids().platform(&[2, 0, 3, 0]).board(&[8, 0]).fits(&merge));
        // A kind present in both shares none.
        assert!(!ids().platform(&[3, 0]).fits(&merge));
        assert!(!ids().platform(&[1, 0]).board(&[9, 0]).fits(&merge));
        // No kind is present in both.
        assert!(!ids().pmic(&[1, 2, 3, 4]).fits(&merge));
        assert!(!TreeIds::default().fits(&merge));
    }

    #[test]
    fn overlap() {
        let merge = ids().platform(&[1, 0, 2, 0]).board(&[8, 0]);
        let overlay = ids().platform(&[2, 0, 3, 0]).pmic(&[1, 2, 3, 4]);
        let overlap = overlay.overlap(&merge).unwrap();
        // The merge lacks the pmic kind and the overlay the board kind.
        assert_eq!(overlap, ids().platform(&[2, 0]));
    }

    #[test]
    fn split() {
        let pmic = [1, 2, 3, 4];
        let merge = ids().platform(&[1, 0, 2, 0]).board(&[8, 0, 9, 0]).pmic(&pmic);
        // The platform and board kinds differ from the overlap: piece a with the overlap's
        // groups, piece b with the remaining groups, piece c equal to b since the pmic kind does
        // not differ, and piece d with only the platform kind's remaining groups.
        let overlap = ids().platform(&[2, 0]).board(&[9, 0]).pmic(&pmic);
        assert_eq!(
            merge.split(&overlap).unwrap(),
            [
                ids().platform(&[2, 0]).board(&[9, 0]).pmic(&pmic),
                ids().platform(&[1, 0]).board(&[8, 0]).pmic(&pmic),
                ids().platform(&[1, 0]).board(&[8, 0, 9, 0]).pmic(&pmic),
            ]
        );
        // The pmic kind differs too: piece c keeps the pmic kind and differs from b.
        let both_pmics = [1, 2, 3, 4, 5, 6, 7, 8];
        let merge_with_pmics = ids().platform(&[1, 0, 2, 0]).board(&[8, 0, 9, 0]).pmic(&both_pmics);
        assert_eq!(
            merge_with_pmics.split(&overlap).unwrap(),
            [
                ids().platform(&[2, 0]).board(&[9, 0]).pmic(&pmic),
                ids().platform(&[1, 0]).board(&[8, 0]).pmic(&[5, 6, 7, 8]),
                ids().platform(&[1, 0]).board(&[8, 0]).pmic(&both_pmics),
                ids().platform(&[1, 0]).board(&[8, 0, 9, 0]).pmic(&both_pmics),
            ]
        );
        // Only the board kind differs: piece c equals piece b, and there is no piece d.
        let overlap = ids().platform(&[1, 0, 2, 0]).board(&[9, 0]);
        assert_eq!(
            merge.split(&overlap).unwrap(),
            [
                ids().platform(&[1, 0, 2, 0]).board(&[9, 0]).pmic(&pmic),
                ids().platform(&[1, 0, 2, 0]).board(&[8, 0]).pmic(&pmic)
            ]
        );
        // Only the platform kind differs, and the merge has no pmic kind: pieces b, c and d are
        // one piece.
        let merge = ids().platform(&[1, 0, 2, 0]).board(&[8, 0]);
        assert_eq!(
            merge.split(&ids().platform(&[2, 0]).board(&[8, 0])).unwrap(),
            [ids().platform(&[2, 0]).board(&[8, 0]), ids().platform(&[1, 0]).board(&[8, 0])]
        );
        // Nothing differs: the merge stays.
        assert!(merge.split(&ids().platform(&[1, 0, 2, 0])).is_none());
    }

    #[test]
    fn split_keeps_the_overlap_order() {
        // The narrowed kind is the overlay's groups and the merge's intersected, which for sets
        // of equal size are in the merge's order.
        let merge =
            ids().platform(&[415, 131072, 540, 131072, 482, 65536, 552, 65536, 552, 131072]);
        let overlay = ids().platform(&[540, 131072, 552, 65536, 457, 0, 415, 131072, 519, 131072]);
        let overlap = overlay.overlap(&merge).unwrap();
        let pieces = merge.split(&overlap).unwrap();
        let platform_values = |ids: &TreeIds| -> Vec<i64> {
            ids.kind(IdKind::Platform).unwrap().iter().flat_map(Group::values).copied().collect()
        };
        assert_eq!(platform_values(&pieces[0]), [415, 131072, 540, 131072, 552, 65536]);
    }

    #[test]
    fn more_specific() {
        let general = ids().platform(&[1, 0, 2, 0, 3, 0]);
        let specific = ids().platform(&[3, 0]);
        assert!(specific.is_more_specific_than(&general));
        assert!(!general.is_more_specific_than(&specific));
        // Equal ids are not more specific.
        assert!(!general.is_more_specific_than(&general.clone()));
        // Ids that lack a kind name every id of it, so they are the more general ones.
        let with_pmic = ids().platform(&[3, 0]).pmic(&[1, 2, 3, 4]);
        assert!(with_pmic.is_more_specific_than(&specific));
        assert!(!specific.is_more_specific_than(&with_pmic));
        // Overlapping ids where neither contains the other.
        assert!(!ids()
            .platform(&[3, 0, 4, 0])
            .is_more_specific_than(&ids().platform(&[2, 0, 3, 0])));
        assert!(!ids().board(&[8, 0]).is_more_specific_than(&specific));
    }

    #[test]
    fn may_apply() {
        let pmic = [1, 2, 3, 4];
        let tree = ids().platform(&[1, 0, 2, 0]).board(&[8, 0]).pmic(&pmic);
        // Every kind shares a group.
        assert!(ids().platform(&[2, 0]).board(&[8, 0]).pmic(&pmic).may_apply_to(&tree));
        // A kind that either file lacks does not count.
        assert!(ids().platform(&[2, 0]).may_apply_to(&tree));
        assert!(ids().platform(&[1, 0]).board(&[9, 0]).may_apply_to(&ids().platform(&[1, 0])));
        // A kind present in both that shares no group.
        assert!(!ids().platform(&[2, 0]).board(&[9, 0]).may_apply_to(&tree));
        // A single all-zero group of the tree takes any overlay.
        assert!(ids().board(&[9, 0]).may_apply_to(&ids().platform(&[1, 0]).board(&[0, 0])));
        assert!(!ids().board(&[9, 0]).may_apply_to(&ids().platform(&[1, 0]).board(&[0, 0, 3, 0])));
        // A tree without a group takes no overlay.
        assert!(!ids().platform(&[1, 0]).may_apply_to(&TreeIds::default()));
    }

    #[test]
    fn from_properties() {
        use IdKind::{Board, Platform, Pmic};
        let path = Path::new("f.dtb");
        let read = |kinds: &[(IdKind, &[i64])], pmic_size: Option<&[i64]>| {
            TreeIds::from_properties(path, &properties(kinds, pmic_size))
        };
        let read_ids = read(
            &[(Platform, &[1, 2, 3, 4]), (Board, &[8, 0]), (Pmic, &[1, 2, 3, 4, 5, 6, 7, 8])],
            None,
        )
        .unwrap();
        // The default pmic group size is 4.
        assert_eq!(
            read_ids,
            ids().platform(&[1, 2, 3, 4]).board(&[8, 0]).pmic(&[1, 2, 3, 4, 5, 6, 7, 8])
        );
        // The pmic group size comes from qcom,pmic-id-size.
        let read_ids = read(&[(Pmic, &[1, 2, 3, 4, 5, 6])], Some(&[3])).unwrap();
        let triples = [[1, 2, 3], [4, 5, 6]].map(|group| Group::new(&group).unwrap());
        assert_eq!(read_ids.kind(Pmic), GroupSet::from_groups(triples).as_ref());
        assert_eq!(read_ids.kind(Platform), None);
        let message = |kinds: &[(IdKind, &[i64])], pmic_size: Option<&[i64]>| {
            read(kinds, pmic_size).unwrap_err().to_string()
        };
        assert_eq!(message(&[(Platform, &[])], None), "f.dtb: qcom,msm-id is empty");
        assert_eq!(
            message(&[(Board, &[8, 0, 9])], None),
            "f.dtb: qcom,board-id has 3 values, which is no multiple of 2"
        );
        assert_eq!(
            message(&[(Pmic, &[1, 2, 3, 4, 5])], Some(&[3])),
            "f.dtb: qcom,pmic-id has 5 values, which is no multiple of 3"
        );
        for (size, shown) in [(&[][..], "[]"), (&[0], "[0]"), (&[-1], "[-1]"), (&[2, 2], "[2, 2]")]
        {
            assert_eq!(
                message(&[(Pmic, &[1, 2])], Some(size)),
                format!("f.dtb: qcom,pmic-id-size is not one positive integer: {shown}")
            );
        }
    }

    #[test]
    fn unsupported_ids() {
        for property in UnsupportedProperty::ALL {
            let mut entries = properties(&[(IdKind::Platform, &[1, 0])], None);
            entries.unsupported = Some(property);
            let error = TreeIds::from_properties(Path::new("f.dtb"), &entries).unwrap_err();
            assert_eq!(
                error.to_string(),
                format!(
                    "f.dtb: the root node names {}, an id that merge_dtbs does not handle",
                    property.property()
                )
            );
        }
    }

    #[test]
    fn name_hash() {
        // CPython's hashes of the tuples of the kinds.
        assert_eq!(TreeIds::default().name_hash(), 0x5fcb_a7fc_6643_e8d2);
        assert_eq!(ids().platform(&[1, 0]).name_hash(), -0x2e89_c762_fe3a_a26c);
        assert_eq!(
            ids().platform(&[1, 0]).board(&[8, 0]).pmic(&[1, 2, 3, 4]).name_hash(),
            -0x7b25_6b06_da7e_2eb5
        );
    }
}
