// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! merge_dtbs merges the device tree overlays of a Qualcomm techpack into the base device trees
//! they apply to.
//!
//! The base files below `--base` and the overlay files below `--techpack` are read for their
//! platform, board and pmic ids. Each base starts one merge. An overlay that fits a merge is
//! applied to it, and where the overlay covers only some of a merge's ids, the merge splits into
//! pieces first. An overlay with ids that are a proper subset of another overlay's applies after
//! that one, so the more specific overlay sets a property both set. The merges are written into
//! `--out`, and every written `.dtbo` is checked to apply on every written `.dtb` it may apply
//! to. The device tree programs fdtget, fdtput, fdtoverlay, fdtoverlaymerge and
//! ufdt_apply_overlay run by name from `PATH`.
//!
//! The written files and their names follow CPython 3.13 where they depend on it: the values of
//! an id property are written in the iteration order of a CPython frozenset of its groups, and
//! the name of a split merge ends in CPython's hash of its ids.

#![forbid(unsafe_code)]

mod arguments;
mod error;
mod group_set;
mod hash;
mod ids;
mod merge;
mod programs;
mod techpack;
mod text;

#[cfg(test)]
mod recorder;
#[cfg(test)]
mod scratch_dir;

use std::env;
use std::ffi::OsString;
use std::io::{self, Write};
use std::path::PathBuf;
use std::process::ExitCode;

use arguments::Directories;
use error::MergeError;
use programs::{Programs, SystemPrograms};
use text::shown;

fn main() -> ExitCode {
    let arguments: Vec<OsString> = env::args_os().skip(1).collect();
    let mut programs = SystemPrograms::new(env::var_os("PATH"));
    let status = run(&arguments, &mut programs, &mut io::stdout(), &mut io::stderr());
    ExitCode::from(status)
}

/// Runs merge_dtbs with the command line arguments after the program name; the exit status: 0
/// on success and for the help, 1 for a failure and 2 for a command line that is no valid one. A
/// failed write to standard output or standard error makes the status 1.
fn run(
    arguments: &[OsString],
    programs: &mut dyn Programs,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> u8 {
    let (written, status) = match arguments::read(arguments) {
        Err(error) => {
            let status = u8::try_from(error.exit_code()).expect("clap's exit statuses are 0 and 2");
            let rendered = error.render();
            let written = if error.use_stderr() {
                write!(stderr, "{rendered}")
            } else {
                write!(stdout, "{rendered}")
            };
            (written, status)
        }
        Ok(directories) => match merge(programs, &directories) {
            Ok(unapplied) => (report_unapplied(stderr, &unapplied), 0),
            Err(error) => (report_failure(stderr, &error), 1),
        },
    };
    match written.and_then(|()| stdout.flush()).and_then(|()| stderr.flush()) {
        Ok(()) => status,
        Err(_) => 1,
    }
}

/// Checks the directories and merges; the overlays that apply to no base.
fn merge(
    programs: &mut dyn Programs,
    directories: &Directories,
) -> Result<Vec<PathBuf>, MergeError> {
    for (option, path) in directories.with_options() {
        if !path.is_dir() {
            return Err(MergeError::NotADirectory { option, path: path.to_owned() });
        }
    }
    techpack::merge_techpack(programs, &directories.base, &directories.techpack, &directories.out)
}

fn report_unapplied(stderr: &mut dyn Write, unapplied: &[PathBuf]) -> io::Result<()> {
    unapplied.iter().try_for_each(|overlay| {
        writeln!(stderr, "merge_dtbs: {} applies to no base device tree", shown(overlay))
    })
}

fn report_failure(stderr: &mut dyn Write, error: &MergeError) -> io::Result<()> {
    if let Some(output) = error.program_output() {
        stderr.write_all(output.as_bytes())?;
    }
    writeln!(stderr, "merge_dtbs: {error}")
}

#[cfg(test)]
mod tests {
    use std::fs;
    use std::path::Path;

    use super::*;
    use crate::programs::{Exit, Program};
    use crate::recorder::Recorder;
    use crate::scratch_dir::ScratchDir;
    use crate::text::shown_text;

    /// Runs merge_dtbs; its exit status, standard output and standard error.
    fn run_with(arguments: &[&Path], programs: &mut Recorder) -> (u8, String, String) {
        let arguments: Vec<OsString> = arguments.iter().map(|&argument| argument.into()).collect();
        let (mut stdout, mut stderr) = (Vec::new(), Vec::new());
        let status = run(&arguments, programs, &mut stdout, &mut stderr);
        (status, String::from_utf8(stdout).unwrap(), String::from_utf8(stderr).unwrap())
    }

    /// Output that cannot be written.
    struct Unwritable;

    impl Write for Unwritable {
        fn write(&mut self, _: &[u8]) -> io::Result<usize> {
            Err(io::Error::from(io::ErrorKind::BrokenPipe))
        }

        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    #[test]
    fn command_line() {
        // The help goes to standard output with status 0.
        let (status, stdout, stderr) = run_with(&[Path::new("-h")], &mut Recorder::default());
        assert_eq!((status, stderr.as_str()), (0, ""));
        assert!(stdout.starts_with("Merges techpack device tree overlays"), "{stdout}");
        assert!(
            stdout.contains("Usage: merge_dtbs --base <BASE> --techpack <TECHPACK> --out <OUT>")
        );
        // A usage error goes to standard error with status 2.
        let (status, stdout, stderr) = run_with(&[Path::new("--base")], &mut Recorder::default());
        assert_eq!((status, stdout.as_str()), (2, ""));
        assert!(stderr.starts_with("error: ") && stderr.contains("--base <BASE>"), "{stderr}");
    }

    #[test]
    fn unwritable_output() {
        let arguments = [OsString::from("-h")];
        let status = run(&arguments, &mut Recorder::default(), &mut Unwritable, &mut Vec::new());
        assert_eq!(status, 1);
        let arguments = [OsString::from("--base")];
        let status = run(&arguments, &mut Recorder::default(), &mut Vec::new(), &mut Unwritable);
        assert_eq!(status, 1);
        // A report of unapplied overlays that cannot be written turns success into status 1.
        let scratch = ScratchDir::new("unwritable_output");
        let base = scratch.subdirectory("base");
        let techpack = scratch.subdirectory("techpack");
        let out = scratch.subdirectory("out");
        let overlay = techpack.join("a.dtbo");
        fs::write(&overlay, "").unwrap();
        let mut programs =
            Recorder::with_files([(overlay.as_path(), vec![("qcom,msm-id", "2 0")])]);
        let arguments = [
            Path::new("--base"),
            &base,
            Path::new("--techpack"),
            &techpack,
            Path::new("--out"),
            &out,
        ]
        .map(OsString::from);
        let status = run(&arguments, &mut programs, &mut Vec::new(), &mut Unwritable);
        assert_eq!(status, 1);
    }

    #[test]
    fn failure_lines() {
        let scratch = ScratchDir::new("failure_lines");
        let base = scratch.subdirectory("base");
        let techpack = scratch.subdirectory("techpack");
        let out = scratch.subdirectory("out");
        let tree = base.join("x.dtb");
        fs::write(&tree, "x").unwrap();
        let arguments = [
            Path::new("--base"),
            &base,
            Path::new("--techpack"),
            &techpack,
            Path::new("--out"),
            &out,
        ];
        // A directory option that names no directory.
        let not_a_directory = [
            Path::new("--base"),
            &tree,
            Path::new("--techpack"),
            &techpack,
            Path::new("--out"),
            &out,
        ];
        assert_eq!(
            run_with(&not_a_directory, &mut Recorder::default()),
            (
                1,
                String::new(),
                format!("merge_dtbs: --base {} is not a directory\n", shown_text(&tree))
            )
        );
        // A root node that fdtget cannot read is one line with fdtget's message.
        let mut programs = Recorder::default().failing(
            Program::Fdtget,
            Exit::Code(1),
            b"Error at '/': FDT_ERR_BADMAGIC\n",
        );
        assert_eq!(
            run_with(&arguments, &mut programs),
            (
                1,
                String::new(),
                format!(
                    "merge_dtbs: {}: cannot read the root node: Error at '/': FDT_ERR_BADMAGIC\n",
                    shown_text(&tree)
                )
            )
        );
        // A program that cannot be started.
        assert_eq!(
            run_with(&arguments, &mut Recorder::default().not_starting(2)),
            (
                1,
                String::new(),
                "merge_dtbs: fdtget: cannot run: No such file or directory (os error 2)\n"
                    .to_owned()
            )
        );
        // A program that fails passes its output on before the failure line.
        let mut programs = Recorder::with_files([(tree.as_path(), vec![("qcom,msm-id", "1 0")])])
            .failing(Program::Fdtput, Exit::Code(1), b"Error: bad\n");
        let written = out.join("x.dtb");
        assert_eq!(
            run_with(&arguments, &mut programs),
            (
                1,
                String::new(),
                format!(
                    "Error: bad\nmerge_dtbs: fdtput -t i {} / qcom,msm-id 1 0 exited with status 1\n",
                    shown_text(&written)
                )
            )
        );
        // The written copy is left in place, and a second run fails on its name.
        let mut programs = Recorder::with_files([(tree.as_path(), vec![("qcom,msm-id", "1 0")])]);
        assert_eq!(
            run_with(&arguments, &mut programs),
            (1, String::new(), format!("merge_dtbs: {} exists already\n", shown_text(&written)))
        );
    }

    #[test]
    fn unapplied_overlays() {
        let scratch = ScratchDir::new("unapplied_overlays");
        let base = scratch.subdirectory("base");
        let techpack = scratch.subdirectory("techpack");
        let out = scratch.subdirectory("out");
        let tree = base.join("x.dtb");
        let overlays = [techpack.join("a.dtbo"), techpack.join("b.dtbo")];
        for path in [&tree, &overlays[0], &overlays[1]] {
            fs::write(path, "").unwrap();
        }
        let mut programs = Recorder::with_files([
            (tree.as_path(), vec![("qcom,msm-id", "1 0")]),
            (overlays[0].as_path(), vec![("qcom,msm-id", "2 0")]),
            (overlays[1].as_path(), vec![("qcom,msm-id", "3 0")]),
        ]);
        let arguments = [
            Path::new("--base"),
            &base,
            Path::new("--techpack"),
            &techpack,
            Path::new("--out"),
            &out,
        ];
        let expected: String = overlays
            .iter()
            .map(|overlay| {
                format!("merge_dtbs: {} applies to no base device tree\n", shown_text(overlay))
            })
            .collect();
        assert_eq!(run_with(&arguments, &mut programs), (0, String::new(), expected));
    }
}
