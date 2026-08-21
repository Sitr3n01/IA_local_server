using System.Collections.Generic;
using UnityEngine;

public class ProjectilePool : MonoBehaviour
{
    [SerializeField] private GameObject prefab;
    [SerializeField] private int prewarmCount;

    private readonly List<GameObject> pool = new List<GameObject>();

    private void Awake()
    {
        if (prefab == null)
        {
            return;
        }

        int count = Mathf.Max(0, prewarmCount);
        for (int i = 0; i < count; i++)
        {
            GameObject instance = Instantiate(prefab);
            instance.SetActive(false);
            pool.Add(instance);
        }
    }

    public GameObject Rent()
    {
        if (pool.Count > 0)
        {
            GameObject instance = pool[pool.Count - 1];
            pool.RemoveAt(pool.Count - 1);
            instance.SetActive(true);
            return instance;
        }

        if (prefab == null)
        {
            return null;
        }

        GameObject instance = Instantiate(prefab);
        instance.SetActive(true);
        return instance;
    }

    public void Return(GameObject go)
    {
        if (go == null)
        {
            return;
        }

        if (!IsInPool(go))
        {
            pool.Add(go);
        }

        go.SetActive(false);
    }

    private bool IsInPool(GameObject go)
    {
        for (int i = 0; i < pool.Count; i++)
        {
            if (pool[i] == go)
            {
                return true;
            }
        }

        return false;
    }
}
