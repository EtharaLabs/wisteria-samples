use openraft::Config;
use openraft::ServerState;

use crate::fixtures::Node;
use crate::fixtures::RaftRouter;
use crate::fixtures::ut_harness;

/// A former leader without a quorum campaigns in a new term and must not surface a
/// cluster-committed log id read back from storage. Only its local committed and
/// applied positions are recovered.
#[tracing::instrument]
#[test_harness::test(harness = ut_harness)]
async fn leader_restart_cluster_committed_not_restored() -> anyhow::Result<()> {
    let config = Config {
        enable_tick: false,
        enable_heartbeat: false,
        enable_elect: false,
        ..Default::default()
    };

    let mut router = RaftRouter::new(config)?;

    tracing::info!("--- bring up a 3-node cluster; node-0 is the leader");
    let log_index = router.new_cluster([0, 1, 2], []).await?;

    tracing::info!(log_index, "--- the live leader has an established cluster commit");
    {
        let m = router.get_metrics(&0)?;
        assert_eq!(
            Some(log_index),
            m.cluster_committed.as_ref().map(|x| x.index()),
            "cluster_committed established by quorum replication"
        );
        assert_eq!(Some(log_index), m.local_committed.as_ref().map(|x| x.index()));
    }

    tracing::info!(
        log_index,
        "--- take both followers offline so the leader loses its quorum"
    );
    for id in [1, 2] {
        router.stop_node(id).await?;
    }

    tracing::info!(
        log_index,
        "--- restart node-0 with its persisted state; it campaigns in a new term"
    );
    {
        let Node {
            log_store: ls,
            state_machine: sm,
            ..
        } = router.stop_node(0).await?;

        router.new_raft_node_with_sto(0, ls, sm).await?;
        router.wait(&0).state(ServerState::Candidate, "campaign without a quorum").await?;
        router.wait(&0).applied_index(Some(log_index), "applied restored from storage").await?;
    }

    tracing::info!(
        log_index,
        "--- local_committed is restored from storage, cluster_committed is not"
    );
    {
        let m = router.get_metrics(&0)?;

        // The committed value persisted in `RaftStorage` is restored into the local apply ceiling.
        assert_eq!(
            Some(log_index),
            m.local_committed.as_ref().map(|x| x.index()),
            "local_committed restored from storage"
        );

        // Without a quorum the restored leader can never re-establish the cluster commit, so the
        // restored value is never promoted to `cluster_committed` nor broadcast to followers.
        assert_eq!(
            None, m.cluster_committed,
            "cluster_committed must not be restored from storage"
        );
    }

    Ok(())
}
