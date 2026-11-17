use std::sync::Arc;
use std::time::Duration;

use maplit::btreeset;
use pretty_assertions::assert_eq;

use crate::Membership;
use crate::MembershipState;
use crate::ServerState;
use crate::Vote;
use crate::engine::Command;
use crate::engine::Engine;
use crate::engine::LogIdList;
use crate::engine::testing::UTConfig;
use crate::engine::testing::log_id;
use crate::raft_state::LogStateReader;
use crate::type_config::TypeConfigExt;
use crate::type_config::alias::StoredMembershipOf;
use crate::utime::Leased;

fn m_empty() -> Membership<u64, ()> {
    Membership::<u64, ()>::new_with_defaults(vec![btreeset! {}], [])
}

fn m23() -> Membership<u64, ()> {
    Membership::<u64, ()>::new_with_defaults(vec![btreeset! {2,3}], [])
}

fn m34() -> Membership<u64, ()> {
    Membership::<u64, ()>::new_with_defaults(vec![btreeset! {3,4}], [])
}

fn eng() -> Engine<UTConfig> {
    let mut eng = Engine::testing_default(0);
    eng.state.enable_validation(false);
    eng.config.id = 2;
    // This will be overridden
    eng.state.server_state = ServerState::default();
    eng
}

/// It is a Leader but not yet append any logs.
#[test]
fn test_startup_as_leader_without_logs() -> anyhow::Result<()> {
    let mut eng = eng();
    // self.id==2 is a voter:
    eng.state.membership_state.set_effective(Arc::new(StoredMembershipOf::<UTConfig>::new(
        Some(log_id(1, 1, 3)),
        m23(),
    )));
    eng.state.log_ids = LogIdList::new(None, [log_id(1, 1, 3)]);
    // A committed vote identifies the former leader.
    eng.state.vote = Leased::new(
        UTConfig::<()>::now(),
        Duration::from_millis(500),
        Vote::new_committed(2, 2),
    );

    eng.startup();

    assert_eq!(ServerState::Candidate, eng.state.server_state);
    assert_eq!(eng.state.vote_ref(), &Vote::new(3, 2));
    assert!(eng.leader_ref().is_none());
    assert_eq!(eng.state.last_log_id(), Some(&log_id(1, 1, 3)));
    assert_eq!(
        vec![
            Command::SaveVote { vote: Vote::new(3, 2) },
            Command::SendVote {
                vote_req: crate::raft::VoteRequest {
                    vote: Vote::new(3, 2),
                    last_log_id: Some(log_id(1, 1, 3)),
                    leadership_transfer_from: None,
                },
            },
        ],
        eng.output.take_commands()
    );

    Ok(())
}

#[test]
fn test_startup_as_leader_with_proposed_logs() -> anyhow::Result<()> {
    tracing::info!("--- a leader proposed logs and restarted, campaign before proposing more logs");
    let mut eng = eng();
    // self.id==2 is a voter:
    eng.state.membership_state.set_effective(Arc::new(StoredMembershipOf::<UTConfig>::new(
        Some(log_id(2, 1, 3)),
        m23(),
    )));
    // Last-per-leader format: leader (1,1) last at index 3, leader (1,2) last at index 6
    eng.state.log_ids = LogIdList::new(None, [log_id(1, 1, 3), log_id(1, 2, 6)]);
    // A committed vote identifies the former leader.
    eng.state.vote = Leased::new(
        UTConfig::<()>::now(),
        Duration::from_millis(500),
        Vote::new_committed(1, 2),
    );

    eng.startup();

    assert_eq!(ServerState::Candidate, eng.state.server_state);
    assert_eq!(eng.state.vote_ref(), &Vote::new(2, 2));
    assert!(eng.leader_ref().is_none());
    assert_eq!(eng.state.last_log_id(), Some(&log_id(1, 2, 6)));
    assert_eq!(
        vec![
            Command::SaveVote { vote: Vote::new(2, 2) },
            Command::SendVote {
                vote_req: crate::raft::VoteRequest {
                    vote: Vote::new(2, 2),
                    last_log_id: Some(log_id(1, 2, 6)),
                    leadership_transfer_from: None,
                },
            },
        ],
        eng.output.take_commands()
    );

    Ok(())
}

/// When starting up, a leader that is not a voter should not panic.
#[test]
fn test_startup_as_leader_not_voter_issue_920() -> anyhow::Result<()> {
    let mut eng = eng();
    // self.id==2 is a voter:
    eng.state.membership_state.set_effective(Arc::new(StoredMembershipOf::<UTConfig>::new(
        Some(log_id(2, 1, 3)),
        m_empty(),
    )));
    // A committed vote identifies the former leader.
    eng.state.vote = Leased::new(
        UTConfig::<()>::now(),
        Duration::from_millis(500),
        Vote::new_committed(1, 2),
    );

    eng.startup();

    assert_eq!(ServerState::Learner, eng.state.server_state);
    assert_eq!(eng.output.take_commands(), vec![]);

    Ok(())
}

#[test]
fn test_startup_candidate_becomes_follower() -> anyhow::Result<()> {
    let mut eng = eng();
    // self.id==2 is a voter:
    eng.state.membership_state.set_effective(Arc::new(StoredMembershipOf::<UTConfig>::new(
        Some(log_id(2, 1, 3)),
        m23(),
    )));
    // Non-committed vote makes it a candidate at startup.
    eng.state.vote = Leased::new(UTConfig::<()>::now(), Duration::from_millis(500), Vote::new(1, 2));

    eng.startup();

    assert_eq!(ServerState::Follower, eng.state.server_state);
    assert_eq!(0, eng.output.take_commands().len());

    Ok(())
}
#[test]
fn test_startup_as_follower() -> anyhow::Result<()> {
    let mut eng = eng();
    // self.id==2 is a voter:
    eng.state.membership_state.set_effective(Arc::new(StoredMembershipOf::<UTConfig>::new(
        Some(log_id(2, 1, 3)),
        m23(),
    )));

    eng.startup();

    assert_eq!(ServerState::Follower, eng.state.server_state);
    assert_eq!(0, eng.output.take_commands().len());

    Ok(())
}

#[test]
fn test_startup_as_learner() -> anyhow::Result<()> {
    let mut eng = eng();
    // self.id==2 is not a voter:
    eng.state.membership_state.set_effective(Arc::new(StoredMembershipOf::<UTConfig>::new(
        Some(log_id(2, 1, 3)),
        m34(),
    )));

    eng.startup();

    assert_eq!(ServerState::Learner, eng.state.server_state);
    assert_eq!(0, eng.output.take_commands().len());

    Ok(())
}

#[test]
fn test_startup_as_leader_leader_restore_disabled() -> anyhow::Result<()> {
    let mut eng = eng();
    eng.config.enable_leader_restore = false;

    // self.id==2 is a voter:
    eng.state.membership_state.set_effective(Arc::new(StoredMembershipOf::<UTConfig>::new(
        Some(log_id(1, 1, 3)),
        m23(),
    )));
    eng.state.log_ids = LogIdList::new(None, [log_id(1, 1, 3)]);
    // Committed vote would normally make it a leader at startup.
    eng.state.vote = Leased::new(
        UTConfig::<()>::now(),
        Duration::from_millis(500),
        Vote::new_committed(2, 2),
    );

    eng.startup();

    // It does not re-elect itself; as a voter it becomes a Follower.
    assert_eq!(ServerState::Follower, eng.state.server_state);
    assert!(eng.leader_ref().is_none());
    // Startup leaves the vote intact in memory and storage, and emits no commands.
    assert_eq!(eng.output.take_commands(), vec![]);
    assert_eq!(eng.state.vote_ref(), &Vote::new_committed(2, 2));

    Ok(())
}

#[test]
fn test_startup_leader_removed_from_effective_membership() {
    for (restore, committed_index, retain_as_learner, expected_state, expected_vote) in [
        (true, 3, false, ServerState::Candidate, Vote::new(3, 2)),
        (false, 3, false, ServerState::Learner, Vote::new_committed(2, 2)),
        (true, 5, false, ServerState::Learner, Vote::new_committed(2, 2)),
        (true, 5, true, ServerState::Candidate, Vote::new(3, 2)),
    ] {
        tracing::info!(
            restore,
            committed_index,
            retain_as_learner,
            ?expected_state,
            "--- restart node 2 with joint {{2,3}},{{3}} at index 3 and final {{3}} at index 5"
        );
        {
            let mut eng = eng();
            eng.config.enable_leader_restore = restore;
            eng.state.log_ids = LogIdList::new(None, [log_id(1, 3, 5)]);
            eng.state.vote = Leased::new(UTConfig::<()>::now(), Duration::ZERO, Vote::new_committed(2, 2));
            eng.state.membership_state = MembershipState::new(
                Arc::new(StoredMembershipOf::<UTConfig>::new(
                    Some(log_id(1, 3, 3)),
                    Membership::new_with_defaults(vec![btreeset! {2,3}, btreeset! {3}], []),
                )),
                Arc::new(StoredMembershipOf::<UTConfig>::new(
                    Some(log_id(1, 3, 5)),
                    Membership::new_with_defaults(vec![btreeset! {3}], retain_as_learner.then_some(2)),
                )),
            );
            eng.state.update_local_committed(&Some(log_id(1, 3, committed_index)));

            eng.startup();

            assert_eq!(expected_state, eng.state.server_state);
            assert_eq!(&expected_vote, eng.state.vote_ref());
            assert!(eng.leader_ref().is_none());
            if expected_state == ServerState::Candidate {
                assert!(eng.candidate_ref().is_some());
            } else {
                assert!(eng.candidate_ref().is_none());
                assert!(eng.output.take_commands().is_empty());
            }
        }
    }
}
