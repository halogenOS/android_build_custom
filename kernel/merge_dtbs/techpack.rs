// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! The merge of a techpack's overlays into the base device trees: the walk of both directories,
//! reading the ids, writing the merges and checking them.

use std::fs::{self, File};
use std::io::{self, ErrorKind};
use std::path::{Path, PathBuf};

use crate::error::MergeError;
use crate::group_set::Group;
use crate::ids::{
    IdKind, PerKind, RootProperties, TreeIds, UnsupportedProperty, PMIC_SIZE_PROPERTY,
};
use crate::merge::{apply_overlay, DeviceTreeFormat, DeviceTreePath, Merge, TreeFile};
use crate::programs::{run_captured, run_program, Invocation, Program, Programs};
use crate::text;

/// Merges the overlays below `techpack` into the bases below `base` and writes the merges into
/// `out`; the overlays that apply to no base, in walk order.
pub fn merge_techpack(
    programs: &mut dyn Programs,
    base: &Path,
    techpack: &Path,
    out: &Path,
) -> Result<Vec<PathBuf>, MergeError> {
    let base_files = collect_tree_files(base)?;
    let overlay_files = collect_tree_files(techpack)?;
    let bases = read_ids_of_all(programs, base_files)?;
    let overlays = read_ids_of_all(programs, overlay_files)?;
    let mut merges: Vec<Vec<Merge>> = bases.iter().map(|base| vec![Merge::new(base)]).collect();
    let mut unapplied = Vec::new();
    for overlay in &overlays {
        let mut applied = false;
        for base_merges in &mut merges {
            applied |= apply_overlay(base_merges, overlay);
        }
        if !applied {
            unapplied.push(overlay.path().to_owned());
        }
    }
    let mut written = Vec::new();
    for base_merges in &merges {
        let single = base_merges.len() == 1;
        for merge in base_merges {
            written.push(emit(programs, merge, out, single)?);
        }
    }
    let written = read_ids_of_all(programs, written)?;
    check(programs, &written)?;
    Ok(unapplied)
}

/// The device tree files below a directory in the order of a sorted top-down walk: the files of a
/// directory in name order, then each subdirectory in name order, walked the same way. A symbolic
/// link to a file counts as a file, and a symbolic link to a directory is not followed.
fn collect_tree_files(top: &Path) -> Result<Vec<DeviceTreePath>, MergeError> {
    let unreadable = |error| MergeError::UnreadableDirectory { path: top.to_owned(), error };
    let mut entries = fs::read_dir(top)
        .map_err(unreadable)?
        .collect::<Result<Vec<_>, _>>()
        .map_err(unreadable)?;
    entries.sort_by_cached_key(|entry| text::sort_key(&entry.file_name()));
    let mut files = Vec::new();
    let mut subdirectories = Vec::new();
    for entry in entries {
        let path = entry.path();
        let unreadable_type = |error| MergeError::UnreadableFileType { path: path.clone(), error };
        let entry_type = match entry.file_type() {
            Ok(entry_type) => entry_type,
            Err(error) if error.kind() == ErrorKind::NotFound => continue,
            Err(error) => return Err(unreadable_type(error)),
        };
        if entry_type.is_dir() {
            subdirectories.push(path);
            continue;
        }
        let is_file = if entry_type.is_symlink() {
            match fs::metadata(&path) {
                Ok(metadata) => metadata.is_file(),
                Err(error) if error.kind() == ErrorKind::NotFound => false,
                Err(error) => return Err(unreadable_type(error)),
            }
        } else {
            entry_type.is_file()
        };
        if is_file {
            files.extend(DeviceTreePath::new(path));
        }
    }
    for subdirectory in subdirectories {
        files.extend(collect_tree_files(&subdirectory)?);
    }
    Ok(files)
}

/// Reads the ids of the files, in their order.
fn read_ids_of_all(
    programs: &mut dyn Programs,
    files: Vec<DeviceTreePath>,
) -> Result<Vec<TreeFile>, MergeError> {
    files
        .into_iter()
        .map(|file| {
            let ids = read_ids(programs, file.path())?;
            Ok(TreeFile::new(file, ids))
        })
        .collect()
}

/// Reads the ids of a device tree file's root node with fdtget. A root node that fdtget cannot
/// read is a failure of one line with fdtget's message.
fn read_ids(programs: &mut dyn Programs, path: &Path) -> Result<TreeIds, MergeError> {
    let root =
        run_captured(programs, &Invocation::new(Program::Fdtget).arg("-p").arg(path).arg("/"))?;
    if !root.exit.is_success() {
        let message = text::joined_lines(&root.output)
            .unwrap_or_else(|| format!("fdtget exited with status {}", root.exit));
        return Err(MergeError::UnreadableRoot { path: path.to_owned(), message });
    }
    let names: Vec<&str> = text::words(&root.output).collect();
    // The values of a property the root node has.
    let mut values_of = |property: &'static str| -> Result<Option<Vec<i64>>, MergeError> {
        if !names.contains(&property) {
            return Ok(None);
        }
        let invocation =
            Invocation::new(Program::Fdtget).args(["-t", "i"]).arg(path).args(["/", property]);
        let output = run_program(programs, invocation)?;
        let words: Vec<&str> = text::words(&output).collect();
        let values = words.iter().map(|word| text::integer(word)).collect::<Option<Vec<_>>>();
        values.map(Some).ok_or_else(|| MergeError::NotAnInteger {
            path: path.to_owned(),
            property,
            words: words.join(" "),
        })
    };
    let ids = PerKind::try_from_fn(|kind| values_of(kind.property()))?;
    let pmic_size = values_of(PMIC_SIZE_PROPERTY)?;
    // The values of every unsupported property the root node has are read too, so that a value
    // that is no integer fails as it does for the other properties.
    let mut unsupported = None;
    for property in UnsupportedProperty::ALL {
        if values_of(property.property())?.is_some() {
            unsupported.get_or_insert(property);
        }
    }
    TreeIds::from_properties(path, &RootProperties { ids, pmic_size, unsupported })
}

/// Writes one merge into the out directory: a copy of the base, or the base with its overlays
/// applied in their application order. Then the merge's ids are written into the written file's
/// root node, each kind's groups in the set's order. The result is the written file.
fn emit(
    programs: &mut dyn Programs,
    merge: &Merge,
    out: &Path,
    single: bool,
) -> Result<DeviceTreePath, MergeError> {
    let written = merge.written_file(out, single);
    let written_path = written.path();
    // Any entry of that name, a broken symbolic link included, fails the merge.
    if fs::symlink_metadata(written_path).is_ok() {
        return Err(MergeError::Exists { path: written_path.to_owned() });
    }
    let base_path = merge.base.path();
    if merge.applied.is_empty() {
        copy_content(base_path, written_path).map_err(|error| MergeError::CopyFailed {
            base: base_path.to_owned(),
            written: written_path.to_owned(),
            error,
        })?;
    } else {
        let overlays = merge.application_order().into_iter().map(TreeFile::path);
        let invocation = Invocation::new(merge.base.format().overlay_program())
            .arg("-i")
            .arg(base_path)
            .args(overlays)
            .arg("-o")
            .arg(written_path);
        run_program(programs, invocation)?;
    }
    for kind in IdKind::ALL {
        if let Some(groups) = merge.ids.kind(kind) {
            let values = groups.iter().flat_map(Group::values).map(i64::to_string);
            let invocation = Invocation::new(Program::Fdtput)
                .args(["-t", "i"])
                .arg(written_path)
                .args(["/", kind.property()])
                .args(values);
            run_program(programs, invocation)?;
        }
    }
    Ok(written)
}

/// Copies a file's content into a new file, which gets the mode of a new file and not the
/// source's.
fn copy_content(from: &Path, to: &Path) -> io::Result<()> {
    let mut source = File::open(from)?;
    let mut target = File::create(to)?;
    io::copy(&mut source, &mut target)?;
    Ok(())
}

/// Checks that every written overlay applies on every written tree it may apply to.
fn check(programs: &mut dyn Programs, written: &[TreeFile]) -> Result<(), MergeError> {
    let of_format = |format| written.iter().filter(move |file| file.format() == format);
    for tree in of_format(DeviceTreeFormat::Dtb) {
        for overlay in of_format(DeviceTreeFormat::Dtbo)
            .filter(|overlay| overlay.ids().may_apply_to(tree.ids()))
        {
            let invocation = Invocation::new(Program::UfdtApplyOverlay)
                .arg(tree.path())
                .arg(overlay.path())
                .arg("/dev/null");
            run_program(programs, invocation)?;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use std::ffi::OsString;
    use std::os::unix::fs::{symlink, PermissionsExt};

    use super::*;
    use crate::ids::ids;
    use crate::programs::Exit;
    use crate::recorder::Recorder;
    use crate::scratch_dir::ScratchDir;
    use crate::text::shown_text;

    /// The fdtput invocation that writes these values, as words.
    fn fdtput(path: &Path, property: &str, values: &[i64]) -> Vec<OsString> {
        let head = ["fdtput", "-t", "i", &shown_text(path), "/", property].map(OsString::from);
        head.into_iter().chain(values.iter().map(|value| value.to_string().into())).collect()
    }

    fn words(words: &[&str]) -> Vec<OsString> {
        words.iter().map(OsString::from).collect()
    }

    /// A file of a format with these ids.
    fn tree_file(path: impl Into<PathBuf>, ids: TreeIds) -> TreeFile {
        TreeFile::new(DeviceTreePath::new(path.into()).unwrap(), ids)
    }

    #[test]
    fn walk_order() {
        let scratch = ScratchDir::new("walk_order");
        let top = scratch.path();
        for name in ["b.dtb", "a.dtbo", "c.txt", ".dtb", "x.DTB", "B.dtb", "é.dtb", "z.dtb"] {
            fs::write(top.join(name), name).unwrap();
        }
        let sub = scratch.subdirectory("sub");
        fs::write(sub.join("a.dtb"), "").unwrap();
        let deeper = scratch.subdirectory("a-dir/deeper");
        fs::write(deeper.join("d.dtbo"), "").unwrap();
        // A directory with a tree's name is walked, not taken.
        scratch.subdirectory("dir.dtb");
        // A link to a file counts, a link to a directory and a broken link do not.
        symlink(top.join("b.dtb"), top.join("link.dtb")).unwrap();
        symlink(&sub, top.join("linked-dir.dtb")).unwrap();
        symlink(top.join("missing"), top.join("broken.dtb")).unwrap();
        let files: Vec<(String, DeviceTreeFormat)> = collect_tree_files(top)
            .unwrap()
            .into_iter()
            .map(|file| (shown_text(file.path().strip_prefix(top).unwrap()), file.format()))
            .collect();
        let expected = [
            ("B.dtb", DeviceTreeFormat::Dtb),
            ("a.dtbo", DeviceTreeFormat::Dtbo),
            ("b.dtb", DeviceTreeFormat::Dtb),
            ("link.dtb", DeviceTreeFormat::Dtb),
            ("z.dtb", DeviceTreeFormat::Dtb),
            ("é.dtb", DeviceTreeFormat::Dtb),
            ("a-dir/deeper/d.dtbo", DeviceTreeFormat::Dtbo),
            ("sub/a.dtb", DeviceTreeFormat::Dtb),
        ]
        .map(|(path, format)| (path.to_owned(), format));
        assert_eq!(files, expected);
    }

    #[test]
    fn walk_failures() {
        let scratch = ScratchDir::new("walk_failures");
        let missing = scratch.path().join("missing");
        assert_eq!(
            collect_tree_files(&missing).unwrap_err().to_string(),
            format!(
                "{}: cannot read the directory: No such file or directory (os error 2)",
                shown_text(&missing)
            )
        );
        // A link that cannot be followed is a failure, even without a tree's name.
        let looping = scratch.path().join("loop.txt");
        symlink(&looping, &looping).unwrap();
        assert_eq!(
            collect_tree_files(scratch.path()).unwrap_err().to_string(),
            format!("{}: Too many levels of symbolic links (os error 40)", shown_text(&looping))
        );
    }

    #[test]
    fn root_node_failures() {
        let path = Path::new("x.dtb");
        for (output, cause) in [
            (&b"Error at '/': FDT_ERR_BADMAGIC\n"[..], "Error at '/': FDT_ERR_BADMAGIC"),
            (b"  first \r\n\n second\x0b\n", "first second"),
            (b"", "fdtget exited with status 1"),
        ] {
            let mut programs = Recorder::default().failing(Program::Fdtget, Exit::Code(1), output);
            assert_eq!(
                read_ids(&mut programs, path).unwrap_err().to_string(),
                format!("x.dtb: cannot read the root node: {cause}")
            );
        }
        let mut programs = Recorder::default().failing(Program::Fdtget, Exit::Signal(11), b"");
        assert_eq!(
            read_ids(&mut programs, path).unwrap_err().to_string(),
            "x.dtb: cannot read the root node: fdtget exited with status -11"
        );
    }

    #[test]
    fn read_values() {
        let path = Path::new("x.dtb");
        let mut programs = Recorder::with_files([(
            path,
            vec![("model", "1"), ("qcom,msm-id", "457 -65536"), ("qcom,board-id", "+8 0")],
        )]);
        assert_eq!(
            read_ids(&mut programs, path).unwrap(),
            ids().platform(&[457, -65536]).board(&[8, 0])
        );
        // Only the id properties are read.
        assert_eq!(
            programs.invocations_of(Program::Fdtget),
            [
                words(&["fdtget", "-p", "x.dtb", "/"]),
                words(&["fdtget", "-t", "i", "x.dtb", "/", "qcom,msm-id"]),
                words(&["fdtget", "-t", "i", "x.dtb", "/", "qcom,board-id"]),
            ]
        );
        let mut programs = Recorder::with_files([(path, vec![("qcom,msm-id", "0x1 0")])]);
        assert_eq!(
            read_ids(&mut programs, path).unwrap_err().to_string(),
            "x.dtb: fdtget printed a value of qcom,msm-id that is no integer: 0x1 0"
        );
    }

    #[test]
    fn read_pmic_size() {
        let path = Path::new("x.dtb");
        let mut programs = Recorder::with_files([(
            path,
            vec![("qcom,pmic-id-size", "3"), ("qcom,pmic-id", "1 2 3 4 5 6")],
        )]);
        let read = read_ids(&mut programs, path).unwrap();
        let groups: Vec<&[i64]> =
            read.kind(IdKind::Pmic).unwrap().iter().map(Group::values).collect();
        assert_eq!(groups.len(), 2);
        assert!(groups.iter().all(|group| group.len() == 3), "{groups:?}");
        assert_eq!(
            programs.invocations_of(Program::Fdtget),
            [
                words(&["fdtget", "-p", "x.dtb", "/"]),
                words(&["fdtget", "-t", "i", "x.dtb", "/", "qcom,pmic-id"]),
                words(&["fdtget", "-t", "i", "x.dtb", "/", "qcom,pmic-id-size"]),
            ]
        );
    }

    #[test]
    fn read_unsupported_property() {
        let path = Path::new("x.dtb");
        let mut programs = Recorder::with_files([(
            path,
            vec![("qcom,msm-id", "1 0"), ("xiaomi,miboard-id", "2"), ("oplus,hw-id", "1")],
        )]);
        // The first unsupported property in lookup order fails the file.
        assert_eq!(
            read_ids(&mut programs, path).unwrap_err().to_string(),
            "x.dtb: the root node names oplus,hw-id, an id that merge_dtbs does not handle"
        );
        assert_eq!(
            programs.invocations_of(Program::Fdtget),
            [
                words(&["fdtget", "-p", "x.dtb", "/"]),
                words(&["fdtget", "-t", "i", "x.dtb", "/", "qcom,msm-id"]),
                words(&["fdtget", "-t", "i", "x.dtb", "/", "oplus,hw-id"]),
                words(&["fdtget", "-t", "i", "x.dtb", "/", "xiaomi,miboard-id"]),
            ]
        );
    }

    #[test]
    fn emit_copy() {
        let scratch = ScratchDir::new("emit_copy");
        let base_path = scratch.path().join("base.dtb");
        fs::write(&base_path, "base bytes").unwrap();
        fs::set_permissions(&base_path, fs::Permissions::from_mode(0o751)).unwrap();
        let out = scratch.subdirectory("out");
        let base = tree_file(
            &base_path,
            ids().platform(&[530, 65536, 531, 65536, 540, 65536]).pmic(&[1, 2, 3, 4]),
        );
        let mut programs = Recorder::default();
        let written = emit(&mut programs, &Merge::new(&base), &out, true).unwrap();
        // A merge without an applied overlay is a copy of the base, byte for byte, with the mode
        // of a new file, and fdtput then writes each kind that has a group, in the kinds' order,
        // with the groups in the set's order.
        let written = written.path();
        assert_eq!(written, out.join("base.dtb"));
        assert_eq!(fs::read(written).unwrap(), b"base bytes");
        let execute_bits = fs::metadata(written).unwrap().permissions().mode() & 0o111;
        assert_eq!(execute_bits, 0);
        assert_eq!(
            programs.invocations.iter().map(ToString::to_string).collect::<Vec<_>>(),
            [
                format!(
                    "fdtput -t i {} / qcom,msm-id 531 65536 540 65536 530 65536",
                    shown_text(written)
                ),
                format!("fdtput -t i {} / qcom,pmic-id 1 2 3 4", shown_text(written)),
            ]
        );
        // A name that exists already fails before anything runs.
        let mut programs = Recorder::default();
        let error = emit(&mut programs, &Merge::new(&base), &out, true).unwrap_err();
        assert_eq!(error.to_string(), format!("{} exists already", shown_text(written)));
        assert!(programs.invocations.is_empty());
        // A copy that fails names both files.
        let missing = scratch.path().join("missing");
        let error = emit(&mut Recorder::default(), &Merge::new(&base), &missing, true).unwrap_err();
        assert_eq!(
            error.to_string(),
            format!(
                "cannot copy {} to {}: No such file or directory (os error 2)",
                shown_text(&base_path),
                shown_text(&missing.join("base.dtb"))
            )
        );
    }

    #[test]
    fn emit_onto_a_broken_link() {
        let scratch = ScratchDir::new("emit_onto_a_broken_link");
        let base_path = scratch.path().join("base.dtb");
        fs::write(&base_path, "base bytes").unwrap();
        let out = scratch.subdirectory("out");
        let written = out.join("base.dtb");
        symlink(scratch.path().join("missing"), &written).unwrap();
        let base = tree_file(&base_path, ids().platform(&[1, 0]));
        let mut programs = Recorder::default();
        let error = emit(&mut programs, &Merge::new(&base), &out, true).unwrap_err();
        assert_eq!(error.to_string(), format!("{} exists already", shown_text(&written)));
        assert!(programs.invocations.is_empty());
        assert!(!scratch.path().join("missing").exists());
    }

    #[test]
    fn emit_copy_fails_after_creating_the_target() {
        // On Linux, a directory opens for reading, and reading it fails.
        let scratch = ScratchDir::new("emit_copy_fails_after_creating_the_target");
        let base_path = scratch.subdirectory("base.dtb");
        let out = scratch.subdirectory("out");
        let base = tree_file(&base_path, ids().platform(&[1, 0]));
        let error = emit(&mut Recorder::default(), &Merge::new(&base), &out, true).unwrap_err();
        let written = out.join("base.dtb");
        assert!(matches!(error, MergeError::CopyFailed { .. }), "{error:?}");
        assert_eq!(
            error.to_string(),
            format!(
                "cannot copy {} to {}: Is a directory (os error 21)",
                shown_text(&base_path),
                shown_text(&written)
            )
        );
        assert!(written.is_file());
    }

    #[test]
    fn emit_overlays() {
        let scratch = ScratchDir::new("emit_overlays");
        let out = scratch.path();
        let merge_ids = ids().platform(&[1, 0]).board(&[8, 0]);
        let a = tree_file("t/a.dtbo", ids().platform(&[1, 0]));
        let b = tree_file("t/b.dtbo", ids().board(&[8, 0]));
        for (base_path, program) in
            [("b/soc.dtb", "fdtoverlay"), ("b/soc-overlay.dtbo", "fdtoverlaymerge")]
        {
            let base = tree_file(base_path, TreeIds::default());
            let merge = Merge { base: &base, ids: merge_ids.clone(), applied: vec![&a, &b] };
            let mut programs = Recorder::default();
            let written = shown_text(emit(&mut programs, &merge, out, true).unwrap().path());
            assert_eq!(
                programs.invocations.iter().map(ToString::to_string).collect::<Vec<_>>(),
                [
                    format!("{program} -i {base_path} t/a.dtbo t/b.dtbo -o {written}"),
                    format!("fdtput -t i {written} / qcom,msm-id 1 0"),
                    format!("fdtput -t i {written} / qcom,board-id 8 0"),
                ]
            );
        }
        // A failing program passes its output on and fails with its command and status.
        let base = tree_file("b/x.dtb", TreeIds::default());
        let merge = Merge { base: &base, ids: merge_ids, applied: vec![&a] };
        let mut programs =
            Recorder::default().failing(Program::Fdtoverlay, Exit::Code(2), b"overlay trouble\r\n");
        let error = emit(&mut programs, &merge, out, false).unwrap_err();
        assert_eq!(error.program_output(), Some("overlay trouble\n"));
        let written = out.join("x-a-0x12daffbf6fd918e8.dtb");
        assert_eq!(
            error.to_string(),
            format!(
                "fdtoverlay -i b/x.dtb t/a.dtbo -o {} exited with status 2",
                shown_text(&written)
            )
        );
    }

    #[test]
    fn emit_application_order() {
        let scratch = ScratchDir::new("emit_application_order");
        let out = scratch.path();
        // ukee-vidc names one platform of the many that waipio-vidc names and comes first in walk
        // order: it applies last, and the name keeps walk order.
        let ukee = tree_file("t/ukee-vidc.dtbo", ids().platform(&[1, 0]));
        let waipio = tree_file("t/waipio-vidc.dtbo", ids().platform(&[1, 0, 2, 0]));
        let base = tree_file("b/soc.dtb", TreeIds::default());
        let merge = Merge {
            base: &base,
            ids: ids().platform(&[1, 0]).board(&[8, 0]),
            applied: vec![&ukee, &waipio],
        };
        let mut programs = Recorder::default();
        let written = emit(&mut programs, &merge, out, false).unwrap();
        assert_eq!(written.path(), out.join("soc-ukee-vidc-waipio-vidc-0x12daffbf6fd918e8.dtb"));
        assert_eq!(
            programs.invocations_of(Program::Fdtoverlay),
            [words(&[
                "fdtoverlay",
                "-i",
                "b/soc.dtb",
                "t/waipio-vidc.dtbo",
                "t/ukee-vidc.dtbo",
                "-o",
                &shown_text(written.path())
            ])]
        );
    }

    #[test]
    fn merge() {
        let scratch = ScratchDir::new("merge");
        let base = scratch.subdirectory("base");
        let techpack = scratch.subdirectory("techpack");
        let out = scratch.subdirectory("out");
        let x = base.join("x.dtb");
        let y = base.join("y.dtbo");
        let p = techpack.join("p.dtbo");
        let q = techpack.join("q.dtbo");
        for path in [&x, &y, &p, &q] {
            fs::write(path, path.file_name().unwrap().as_encoded_bytes()).unwrap();
        }
        let mut programs = Recorder::with_files([
            (x.as_path(), vec![("qcom,msm-id", "1 0 2 0")]),
            (y.as_path(), vec![("qcom,msm-id", "1 0")]),
            (p.as_path(), vec![("qcom,msm-id", "2 0")]),
            (q.as_path(), vec![("qcom,msm-id", "9 0")]),
        ]);
        let unapplied = merge_techpack(&mut programs, &base, &techpack, &out).unwrap();
        // x.dtb splits for p.dtbo into the piece p applies to and the rest, which is a copy;
        // y.dtbo, which p does not fit, keeps its name; q.dtbo applies to no base.
        let x_narrow = out.join("x-p-0x843b331131325f0.dtb");
        let x_rest = out.join("x--0x2e89c762fe3aa26c.dtb");
        let y_written = out.join("y.dtbo");
        assert_eq!(unapplied, [q]);
        assert_eq!(
            programs.invocations_of(Program::Fdtoverlay),
            [words(&[
                "fdtoverlay",
                "-i",
                &shown_text(&x),
                &shown_text(&p),
                "-o",
                &shown_text(&x_narrow)
            ])]
        );
        assert!(programs.invocations_of(Program::Fdtoverlaymerge).is_empty());
        assert_eq!(
            programs.invocations_of(Program::Fdtput),
            [
                fdtput(&x_narrow, "qcom,msm-id", &[2, 0]),
                fdtput(&x_rest, "qcom,msm-id", &[1, 0]),
                fdtput(&y_written, "qcom,msm-id", &[1, 0]),
            ]
        );
        let mut names: Vec<_> =
            fs::read_dir(&out).unwrap().map(|entry| entry.unwrap().file_name()).collect();
        names.sort();
        assert_eq!(names, ["x--0x2e89c762fe3aa26c.dtb", "y.dtbo"]);
        assert_eq!(fs::read(&x_rest).unwrap(), b"x.dtb");
        // Only the rest of x.dtb shares a platform id with y.dtbo, so only that pair is checked.
        assert_eq!(
            programs.invocations_of(Program::UfdtApplyOverlay),
            [words(&[
                "ufdt_apply_overlay",
                &shown_text(&x_rest),
                &shown_text(&y_written),
                "/dev/null"
            ])]
        );
    }
}
